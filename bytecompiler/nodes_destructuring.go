package bytecompiler

import (
	"slices"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Code generation for destructuring (NodesCodegen.cpp,
// DestructuringAssignmentNode through ObjectSpreadExpressionNode).

func (g *Generator) genDestructuringAssignment(n *parser.DestructuringAssignmentNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	initializer := g.keep(g.tempDestination(dst))
	g.emitNode(initializer, n.Initializer)
	g.bindValue(n.Bindings, initializer)
	return g.move(dst, initializer)
}

func (g *Generator) assignDefaultValueIfUndefined(maybeUndefined *RegisterID, defaultValue parser.ExpressionNode) {
	isNotUndefined := g.newLabel()
	g.emitJumpIfFalse(g.emitIsUndefined(g.newTemporary(), maybeUndefined), isNotUndefined)
	g.emitNode(maybeUndefined, defaultValue)
	g.emitLabel(isNotUndefined)
}

// bindValue is DestructuringPatternNode::bindValue: it stores value into
// the pattern's targets.
func (g *Generator) bindValue(pattern parser.DestructuringPatternNode, value *RegisterID) {
	switch p := pattern.(type) {
	case *parser.ArrayPatternNode:
		g.bindArrayPattern(p, value)
	case *parser.ObjectPatternNode:
		g.bindObjectPattern(p, value)
	case *parser.BindingNode:
		g.bindBinding(p, value)
	case *parser.AssignmentElementNode:
		g.bindAssignmentElement(p, value)
	default:
		panic("bytecompiler: cannot bind a value to this pattern")
	}
}

// bindValueCanThrow reports whether storing into the pattern may throw.
func (g *Generator) bindValueCanThrow(pattern parser.DestructuringPatternNode) bool {
	switch p := pattern.(type) {
	case *parser.BindingNode:
		v := g.variable(p.BoundProperty, ThisResolutionLocal)
		if v.Offset().IsStack() || v.Offset().IsScope() {
			if p.BindingContext != parser.AssignmentContextConstDeclarationStatement && v.IsReadOnly() {
				return true
			}
			if p.BindingContext == parser.AssignmentContextAssignmentExpression && g.needsTDZCheck(v) {
				return true
			}
			return false
		}
		return true
	case *parser.AssignmentElementNode:
		if lhs, ok := p.AssignmentTarget.(*parser.ResolveNode); ok {
			v := g.variable(lhs.Ident, ThisResolutionLocal)
			if v.Offset().IsStack() || v.Offset().IsScope() {
				return v.IsReadOnly() || g.needsTDZCheck(v)
			}
		}
		return true
	}
	return true
}

// writableDirectBindingIfPossible returns the local the pattern binds if a
// value can be written to it directly.
func (g *Generator) writableDirectBindingIfPossible(pattern parser.DestructuringPatternNode) *RegisterID {
	switch p := pattern.(type) {
	case *parser.BindingNode:
		v := g.variable(p.BoundProperty, ThisResolutionLocal)
		isReadOnly := v.IsReadOnly() && p.BindingContext != parser.AssignmentContextConstDeclarationStatement
		if local := v.Local(); local != nil {
			if p.BindingContext == parser.AssignmentContextAssignmentExpression && g.needsTDZCheck(v) {
				return nil
			}
			if isReadOnly {
				return nil
			}
			return local
		}
	case *parser.AssignmentElementNode:
		lhs, ok := p.AssignmentTarget.(*parser.ResolveNode)
		if !ok {
			return nil
		}
		v := g.variable(lhs.Ident, ThisResolutionLocal)
		if local := v.Local(); local != nil {
			if g.needsTDZCheck(v) || v.IsReadOnly() {
				return nil
			}
			return local
		}
	}
	return nil
}

// finishDirectBindingAssignment completes a write done through
// writableDirectBindingIfPossible.
func (g *Generator) finishDirectBindingAssignment(pattern parser.DestructuringPatternNode) {
	switch p := pattern.(type) {
	case *parser.BindingNode:
		v := g.variable(p.BoundProperty, ThisResolutionLocal)
		g.emitProfileTypeVariable(v.Local(), v, p.DivotStart, p.DivotEnd)
		if p.BindingContext == parser.AssignmentContextDeclarationStatement || p.BindingContext == parser.AssignmentContextConstDeclarationStatement {
			g.liftTDZCheckIfPossible(v)
		}
	case *parser.AssignmentElementNode:
		lhs := p.AssignmentTarget.(*parser.ResolveNode)
		v := g.variable(lhs.Ident, ThisResolutionLocal)
		g.emitProfileTypeRange(v.Local(), p.DivotStart, p.DivotEnd)
	}
}

func (g *Generator) bindArrayPattern(p *parser.ArrayPatternNode, rhs *RegisterID) {
	if !g.enterRecursion() {
		g.emitThrowExpressionTooDeepException()
		return
	}
	defer g.leaveRecursion()
	defer g.refScope()()

	iterable := g.keep(g.move(g.tempDestination(rhs), rhs))
	iterator := g.keep(g.newTemporary())
	nextOrIndex := g.keep(g.newTemporary())
	g.scoped(func() {
		g.emitExpressionInfo(p.Divot, p.DivotStart, p.DivotEnd)
		iteratorSymbol := g.keep(g.emitGetByID(g.newTemporary(), iterable, ident.IteratorSymbol))
		args := g.newCallArguments(nil, 0)
		g.move(args.thisRegister(), iterable)
		g.emitIteratorOpen(iterator, nextOrIndex, iteratorSymbol, args, &p.ThrowableExpressionData)
	})

	if len(p.TargetPatterns) == 0 {
		g.emitIteratorClose(iterator, nextOrIndex, iterable, &p.ThrowableExpressionData)
		return
	}

	bindValueOrDefaultValueCanThrow := slices.ContainsFunc(p.TargetPatterns, func(target parser.ArrayPatternEntry) bool {
		if target.Pattern != nil && g.bindValueCanThrow(target.Pattern) {
			return true
		}
		if target.DefaultValue != nil {
			if parser.IsConstant(target.DefaultValue) {
				return false
			}
			if r, ok := target.DefaultValue.(*parser.ResolveNode); ok && !g.getFromScopeCanThrow(r) {
				return false
			}
			return true
		}
		return false
	})

	done := g.keep(g.newTemporary())

	emitBindValue := func(g *Generator) {
		for i, target := range p.TargetPatterns {
			g.scoped(func() {
				var targetBase, targetProperty *RegisterID
				var hasTargetNodes bool
				if element, ok := target.Pattern.(*parser.AssignmentElementNode); ok {
					targetBase, targetProperty, hasTargetNodes = g.emitNodesForDestructuring(element, nil, nil)
				}

				// When binding or a default value can throw, done is set to
				// true before each next() (IteratorNext steps 3-5): if next()
				// throws, iteratorRecord.[[Done]] is true and IteratorClose is
				// skipped.
				switch target.BindingType {
				case parser.ArrayPatternElision, parser.ArrayPatternElement:
					iterationSkipped := g.newLabel()
					if i > 0 {
						g.emitJumpIfTrue(done, iterationSkipped)
					}

					value := g.keep(g.newTemporary())
					g.scoped(func() {
						valueIsSet := g.newLabel()
						nextArgs := g.newCallArguments(nil, 0)
						g.move(nextArgs.thisRegister(), iterator)
						if bindValueOrDefaultValueCanThrow {
							g.emitLoad(done, bytecode.Bool(true))
						}
						g.emitIteratorNext(done, value, iterable, nextOrIndex, nextArgs, &p.ThrowableExpressionData)
						g.emitJumpIfFalse(done, valueIsSet)
						g.emitLabel(iterationSkipped)
						g.emitLoad(value, bytecode.Undefined{})
						g.emitLabel(valueIsSet)
					})

					if target.BindingType == parser.ArrayPatternElement {
						if target.DefaultValue != nil {
							g.assignDefaultValueIfUndefined(value, target.DefaultValue)
						}
						if hasTargetNodes {
							g.bindValueWithEmittedNodes(target.Pattern.(*parser.AssignmentElementNode), targetBase, targetProperty, value)
						} else {
							g.bindValue(target.Pattern, value)
						}
					}

				case parser.ArrayPatternRestElement:
					array := g.keep(g.emitNewArray(g.newTemporary(), nil, 0, bytecode.ArrayWithUndecided))

					iterationDone := g.newLabel()
					if i > 0 {
						g.emitJumpIfTrue(done, iterationDone)
					}

					index := g.keep(g.newTemporary())
					g.emitLoad(index, bytecode.JSInt32(0))
					loopStart := g.newLabel()
					g.emitLabel(loopStart)

					value := g.keep(g.newTemporary())
					g.scoped(func() {
						nextArgs := g.newCallArguments(nil, 0)
						g.move(nextArgs.thisRegister(), iterator)
						if bindValueOrDefaultValueCanThrow {
							g.emitLoad(done, bytecode.Bool(true))
						}
						g.emitIteratorNext(done, value, iterable, nextOrIndex, nextArgs, &p.ThrowableExpressionData)
						g.emitJumpIfTrue(done, iterationDone)
					})

					g.emitDirectPutByVal(array, index, value)
					g.emitInc(index)
					g.emitJump(loopStart)

					g.emitLabel(iterationDone)
					if hasTargetNodes {
						g.bindValueWithEmittedNodes(target.Pattern.(*parser.AssignmentElementNode), targetBase, targetProperty, array)
					} else {
						g.bindValue(target.Pattern, array)
					}
				}
			})
		}
	}

	emitIteratorClose := func(g *Generator) {
		iteratorClosed := g.newLabel()
		g.emitJumpIfTrue(done, iteratorClosed)
		g.emitIteratorClose(iterator, nextOrIndex, iterable, &p.ThrowableExpressionData)
		g.emitLabel(iteratorClosed)
	}

	if bindValueOrDefaultValueCanThrow {
		g.emitLoad(done, bytecode.Bool(false))
		g.emitTryWithFinallyThatDoesNotShadowException(emitBindValue, emitIteratorClose)
	} else {
		emitBindValue(g)
		emitIteratorClose(g)
	}
}

func (g *Generator) bindObjectPattern(p *parser.ObjectPatternNode, rhs *RegisterID) {
	if !g.enterRecursion() {
		g.emitThrowExpressionTooDeepException()
		return
	}
	defer g.leaveRecursion()

	firstPropertyName := ident.Null
	if len(p.TargetPatterns) > 0 {
		firstTarget := p.TargetPatterns[0]
		if firstTarget.PropertyExpression == nil && !firstTarget.PropertyName.IsNull() && !firstTarget.PropertyName.IsPrivateName() {
			firstPropertyName = firstTarget.PropertyName
		}
	}
	g.emitRequireObjectCoercibleForDestructuring(rhs, firstPropertyName)

	var preservedTDZStack PreservedTDZStack
	g.preserveTDZStack(&preservedTDZStack)

	g.scoped(func() {
		var restElementBase, restElementPropertyName, newObject *RegisterID
		var excludedSet []ident.Identifier
		var args *CallArguments
		numberOfComputedProperties := 0
		indexInArguments := 2
		if p.ContainsRestElement {
			if p.ContainsComputedProperty {
				for _, target := range p.TargetPatterns {
					if target.BindingType == parser.ObjectPatternElement && target.PropertyExpression != nil {
						numberOfComputedProperties++
					}
				}
			}
			restElementBase = g.keep(g.newTemporary())
			restElementPropertyName = g.keep(g.newTemporary())
			newObject = g.keep(g.newTemporary())
			args = g.newCallArguments(nil, indexInArguments+numberOfComputedProperties)
		}

		for _, target := range p.TargetPatterns {
			g.scoped(func() {
				if target.BindingType == parser.ObjectPatternElement {
					// Store straight into a local binding when possible
					// (get_by_id into the local instead of get_by_id + mov).
					writableDirectBinding := func() *RegisterID {
						// ({ [data]: data, ...obj } = object) must keep the
						// old key to add it to the excluded set.
						if p.ContainsRestElement && p.ContainsComputedProperty && target.PropertyExpression != nil {
							return nil
						}
						// A default value may read the local: ({ data = data } = object).
						if target.DefaultValue != nil && !parser.IsConstant(target.DefaultValue) {
							return nil
						}
						return g.writableDirectBindingIfPossible(target.Pattern)
					}

					var temp *RegisterID
					directBinding := writableDirectBinding()
					if directBinding != nil {
						temp = g.keep(directBinding)
					} else {
						temp = g.keep(g.newTemporary())
					}

					var targetBase, targetProperty *RegisterID
					var hasTargetNodes bool
					element, isElement := target.Pattern.(*parser.AssignmentElementNode)
					if target.PropertyExpression == nil {
						if isElement {
							targetBase, targetProperty, hasTargetNodes = g.emitNodesForDestructuring(element, nil, nil)
						}
						if index, isIndex := target.PropertyName.ParseIndex(); !isIndex {
							g.emitGetByID(temp, rhs, target.PropertyName)
						} else {
							propertyIndex := g.keep(g.emitLoad(nil, bytecode.JSNumber(float64(index))))
							g.emitGetByVal(temp, rhs, propertyIndex)
						}
						if p.ContainsRestElement && !slices.Contains(excludedSet, target.PropertyName) {
							excludedSet = append(excludedSet, target.PropertyName)
						}
					} else {
						var propertyName *RegisterID
						if p.ContainsRestElement {
							propertyName = g.keep(g.emitNodeForProperty(args.argumentRegister(indexInArguments), target.PropertyExpression))
						} else {
							propertyName = g.keep(g.emitNodeForProperty(nil, target.PropertyExpression))
						}
						if !parser.IsNumber(target.PropertyExpression) && !parser.IsString(target.PropertyExpression) {
							// ToPropertyKey of a number or string has no side effects,
							// and get_by_val is faster with numbers.
							var keyDst *RegisterID
							if p.ContainsRestElement {
								keyDst = args.argumentRegister(indexInArguments)
							} else {
								keyDst = g.newTemporary()
							}
							propertyName = g.keep(g.emitToPropertyKeyOrNumber(keyDst, propertyName))
						}
						if p.ContainsRestElement {
							indexInArguments++
						}
						if isElement {
							targetBase, targetProperty, hasTargetNodes = g.emitNodesForDestructuring(element, nil, nil)
						}
						g.emitGetByVal(temp, rhs, propertyName)
					}

					if target.DefaultValue != nil {
						g.assignDefaultValueIfUndefined(temp, target.DefaultValue)
					}

					switch {
					case directBinding != nil:
						g.finishDirectBindingAssignment(target.Pattern)
					case hasTargetNodes:
						g.bindValueWithEmittedNodes(element, targetBase, targetProperty, temp)
					default:
						g.bindValue(target.Pattern, temp)
					}
					return
				}

				// The rest element, which is last.
				var targetBase, targetProperty *RegisterID
				var hasTargetNodes bool
				element, isElement := target.Pattern.(*parser.AssignmentElementNode)
				if isElement {
					targetBase, targetProperty, hasTargetNodes = g.emitNodesForDestructuring(element, restElementBase, restElementPropertyName)
				}

				g.emitNewObject(newObject)

				copyDataProperties := g.keep(g.moveLinkTimeConstant(nil, bytecode.LinkTimeConstantCopyDataProperties))

				// Not a tail call: @copyDataProperties reads the caller frame.
				g.move(args.thisRegister(), newObject)
				g.move(args.argumentRegister(0), rhs)
				g.emitLoadIdentifierSet(args.argumentRegister(1), excludedSet)
				g.emitCallIgnoreResult(g.newTemporary(), copyDataProperties, NoExpectedFunction, args, p.Divot, p.DivotStart, p.DivotEnd, DebuggableCallNo)

				if hasTargetNodes {
					g.bindValueWithEmittedNodes(element, targetBase, targetProperty, newObject)
				} else {
					g.bindValue(target.Pattern, newObject)
				}
			})
		}
	})

	g.restoreTDZStack(&preservedTDZStack)
}

func (g *Generator) bindBinding(b *parser.BindingNode, value *RegisterID) {
	defer g.refScope()()
	v := g.variable(b.BoundProperty, ThisResolutionLocal)
	context := b.BindingContext
	isReadOnly := v.IsReadOnly() && context != parser.AssignmentContextConstDeclarationStatement && !isUsingOrAwaitUsingAssignmentContext(context)
	isAwaitUsing := context == parser.AssignmentContextAwaitUsingDeclarationStatement
	if local := v.Local(); local != nil {
		if context == parser.AssignmentContextAssignmentExpression {
			g.emitTDZCheckIfNecessary(v, local, nil)
		}
		if isReadOnly {
			g.emitReadOnlyExceptionIfNeeded(v)
			return
		}
		g.move(local, value)
		g.emitProfileTypeVariable(local, v, b.DivotStart, b.DivotEnd)
		if isUsingOrAwaitUsingAssignmentContext(context) {
			g.emitPrepareDisposable(local, b.DivotStart, isAwaitUsing)
		}
		if context != parser.AssignmentContextAssignmentExpression {
			g.liftTDZCheckIfPossible(v)
		}
		return
	}
	if g.ecmaMode.IsStrict() {
		g.emitExpressionInfo(b.DivotEnd, b.DivotStart, b.DivotEnd)
	}
	scope := g.keep(g.emitResolveScope(nil, v))
	g.emitExpressionInfo(b.DivotEnd, b.DivotStart, b.DivotEnd)
	if context == parser.AssignmentContextAssignmentExpression {
		g.emitTDZCheckIfNecessary(v, nil, scope)
	}
	if isReadOnly {
		g.emitReadOnlyExceptionIfNeeded(v)
		return
	}
	g.emitPutToScope(scope, v, value, g.resolveModeForPut(), initializationModeForAssignmentContext(context))
	g.emitProfileTypeVariable(value, v, b.DivotStart, b.DivotEnd)
	if isUsingOrAwaitUsingAssignmentContext(context) {
		g.emitPrepareDisposable(value, b.DivotStart, isAwaitUsing)
	}
	if context != parser.AssignmentContextAssignmentExpression {
		g.liftTDZCheckIfPossible(v)
	}
}

// emitNodesForDestructuring evaluates the base (and key) of a.b or a[k]
// assignment targets before the value is read; the registers are held by
// the caller's refScope.
func (g *Generator) emitNodesForDestructuring(e *parser.AssignmentElementNode, base, propertyName *RegisterID) (*RegisterID, *RegisterID, bool) {
	switch t := e.AssignmentTarget.(type) {
	case *parser.DotAccessorNode:
		if base == nil {
			base = g.keep(g.newTemporary())
		}
		g.emitNode(base, t.Base)
		g.emitExpressionInfo(e.DivotEnd, e.DivotStart, e.DivotEnd)
		return base, nil, true
	case *parser.BracketAccessorNode:
		if base == nil {
			base = g.keep(g.newTemporary())
		}
		if propertyName == nil {
			propertyName = g.keep(g.newTemporary())
		}
		g.emitNode(base, t.Base)
		g.emitNodeForProperty(propertyName, t.Subscript)
		g.emitExpressionInfo(e.DivotEnd, e.DivotStart, e.DivotEnd)
		return base, propertyName, true
	}
	return nil, nil, false
}

func (g *Generator) bindValueWithEmittedNodes(e *parser.AssignmentElementNode, base, propertyName, value *RegisterID) {
	defer g.refScope()()
	switch t := e.AssignmentTarget.(type) {
	case *parser.DotAccessorNode:
		var thisValue *RegisterID
		g.emitPutProperty(dotOf(t), base, value, &thisValue)
		g.emitProfileTypeRange(value, e.DivotStart, e.DivotEnd)
	case *parser.BracketAccessorNode:
		if isSuperNode(t.Base) {
			thisValue := g.keep(g.ensureThis())
			g.emitPutByValWithThis(base, thisValue, propertyName, value)
		} else {
			g.emitPutByVal(base, propertyName, value)
		}
		g.emitProfileTypeRange(value, e.DivotStart, e.DivotEnd)
	}
}

func (g *Generator) bindAssignmentElement(e *parser.AssignmentElementNode, value *RegisterID) {
	defer g.refScope()()
	switch lhs := e.AssignmentTarget.(type) {
	case *parser.ResolveNode:
		v := g.variable(lhs.Ident, ThisResolutionLocal)
		isReadOnly := v.IsReadOnly()
		if local := v.Local(); local != nil {
			g.emitTDZCheckIfNecessary(v, local, nil)
			if isReadOnly {
				g.emitReadOnlyExceptionIfNeeded(v)
			} else {
				g.move(local, value)
				g.emitProfileTypeRange(local, e.DivotStart, e.DivotEnd)
			}
			return
		}
		if g.ecmaMode.IsStrict() {
			g.emitExpressionInfo(e.DivotEnd, e.DivotStart, e.DivotEnd)
		}
		scope := g.keep(g.emitResolveScope(nil, v))
		g.emitTDZCheckIfNecessary(v, nil, scope)
		if isReadOnly {
			if g.emitReadOnlyExceptionIfNeeded(v) {
				return
			}
		}
		g.emitExpressionInfo(e.DivotEnd, e.DivotStart, e.DivotEnd)
		if !isReadOnly {
			g.emitPutToScope(scope, v, value, g.resolveModeForPut(), bytecode.InitializationModeNotInitialization)
			g.emitProfileTypeVariable(value, v, e.DivotStart, e.DivotEnd)
		}
	case *parser.DotAccessorNode:
		base := g.emitNodeForLeftHandSide(lhs.Base, true, false)
		g.emitExpressionInfo(e.DivotEnd, e.DivotStart, e.DivotEnd)
		var thisValue *RegisterID
		g.emitPutProperty(dotOf(lhs), base, value, &thisValue)
		g.emitProfileTypeRange(value, e.DivotStart, e.DivotEnd)
	case *parser.BracketAccessorNode:
		base := g.emitNodeForLeftHandSide(lhs.Base, true, false)
		property := g.emitNodeForLeftHandSideForProperty(lhs.Subscript, true, false)
		g.emitExpressionInfo(e.DivotEnd, e.DivotStart, e.DivotEnd)
		if isSuperNode(lhs.Base) {
			thisValue := g.keep(g.ensureThis())
			g.emitPutByValWithThis(base, thisValue, property, value)
		} else {
			g.emitPutByVal(base, property, value)
		}
		g.emitProfileTypeRange(value, e.DivotStart, e.DivotEnd)
	}
}

// emitRestParameterNode is RestParameterNode::emit.
func (g *Generator) emitRestParameterNode(r *parser.RestParameterNode) {
	if directBinding := g.writableDirectBindingIfPossible(r.Pattern); directBinding != nil {
		g.emitRestParameter(directBinding, r.NumParametersToSkip)
		g.finishDirectBindingAssignment(r.Pattern)
		return
	}
	defer g.refScope()()
	temp := g.keep(g.newTemporary())
	g.emitRestParameter(temp, r.NumParametersToSkip)
	g.bindValue(r.Pattern, temp)
}

// genObjectSpread copies the properties of the spread expression into dst
// (the object literal being built).
func (g *Generator) genObjectSpread(n *parser.ObjectSpreadExpressionNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	src := g.keep(g.newTemporary())
	g.emitNode(src, n.Expression)

	copyDataProperties := g.keep(g.moveLinkTimeConstant(nil, bytecode.LinkTimeConstantCopyDataProperties))

	args := g.newCallArguments(nil, 1)
	g.move(args.thisRegister(), dst)
	g.move(args.argumentRegister(0), src)

	// Not a tail call: @copyDataProperties reads the caller frame.
	g.emitCallIgnoreResult(g.newTemporary(), copyDataProperties, NoExpectedFunction, args, n.Divot, n.DivotStart, n.DivotEnd, DebuggableCallNo)
	return dst
}
