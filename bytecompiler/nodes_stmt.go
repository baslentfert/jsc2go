package bytecompiler

import (
	"math"

	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Code generation for statements (NodesCodegen.cpp, SourceElements through
// TryNode).

// emitSourceElements emits a statement list; dst receives the completion
// value when the code needs it (eval and programs).
func (g *Generator) emitSourceElements(s *parser.SourceElements, dst *RegisterID) {
	var lastStatementWithCompletionValue parser.StatementNode
	if g.shouldBeConcernedWithCompletionValue() {
		for _, statement := range s.Statements {
			if parser.HasCompletionValue(statement) {
				lastStatementWithCompletionValue = statement
			}
		}
	}

	for _, statement := range s.Statements {
		if g.shouldBeConcernedWithCompletionValue() {
			if statement == lastStatementWithCompletionValue {
				g.emitLoad(dst, bytecode.Undefined{})
			}
			g.emitStatementInTailPosition(dst, statement)
		} else {
			g.emitStatementInTailPosition(g.IgnoredResult(), statement)
		}
	}
}

func (g *Generator) genBlock(n *parser.BlockNode, dst *RegisterID) {
	if n.Statements == nil {
		return
	}
	g.pushLexicalScope(&n.VariableEnvironmentNode, ScopeTypeLetConstScope, TDZCheckOptimize, NestedScopeIsNested, nil, true)

	g.emitBodyWithUsingIfNeeded(n.UsingDeclarationCount(), n.HasAwaitUsingDeclaration(), func(g *Generator) {
		g.emitSourceElements(n.Statements, dst)
	})

	g.popLexicalScope(&n.VariableEnvironmentNode)
}

func (g *Generator) genDebuggerStatement(n *parser.DebuggerStatementNode) {
	g.emitDebugHookAt(bytecode.DidReachDebuggerStatement, n.Position(), nil)
}

func (g *Generator) genExprStatement(n *parser.ExprStatementNode, dst *RegisterID) {
	g.emitNodeInTailPositionFromExprStatementNode(dst, n.Expr)
}

func (g *Generator) genDeclarationStatement(n *parser.DeclarationStatement) {
	g.emitNode(nil, n.Expr)
}

// genEmptyVarExpression only matters for the type profiler; the result of
// a declaration is ignored.
func (g *Generator) genEmptyVarExpression(n *parser.EmptyVarExpression) *RegisterID {
	if !g.shouldEmitTypeProfilerHooks() {
		return nil
	}
	defer g.refScope()()
	v := g.variable(n.Ident, ThisResolutionLocal)
	end := n.Position() + identLength(n.Ident)
	if local := v.Local(); local != nil {
		g.emitProfileTypeVariable(local, v, n.Position(), end)
	} else {
		scope := g.keep(g.emitResolveScope(nil, v))
		value := g.keep(g.emitGetFromScope(g.newTemporary(), scope, v, bytecode.DoNotThrowIfNotFound))
		g.emitProfileTypeVariable(value, v, n.Position(), end)
	}
	return nil
}

// genEmptyLetExpression initializes `let x;` to undefined so that later
// reads don't hit the TDZ.
func (g *Generator) genEmptyLetExpression(n *parser.EmptyLetExpression) *RegisterID {
	defer g.refScope()()
	v := g.variable(n.Ident, ThisResolutionLocal)
	end := n.Position() + identLength(n.Ident)
	if local := v.Local(); local != nil {
		g.emitLoad(local, bytecode.Undefined{})
		g.emitProfileTypeVariable(local, v, n.Position(), end)
	} else {
		scope := g.keep(g.emitResolveScope(nil, v))
		value := g.keep(g.emitLoad(nil, bytecode.Undefined{}))
		g.emitPutToScope(scope, v, value, g.resolveModeForPut(), bytecode.InitializationModeInitialization)
		g.emitProfileTypeVariable(value, v, n.Position(), end)
	}

	g.liftTDZCheckIfPossible(v)
	return nil
}

// --- if / loops ---------------------------------------------------------------------

func singleStatement(s parser.StatementNode) parser.StatementNode {
	if b, ok := s.(*parser.BlockNode); ok {
		if b.Statements == nil {
			return nil
		}
		return b.Statements.SingleStatement()
	}
	return s
}

func isBlock(s parser.StatementNode) bool {
	_, ok := s.(*parser.BlockNode)
	return ok
}

// blockEndOffset is the profiler offset just past a statement.
func blockEndOffset(s parser.StatementNode) int {
	if isBlock(s) {
		return s.BaseNode().EndOffset() + 1
	}
	return s.BaseNode().EndOffset()
}

// trivialBreakTarget is BreakNode::trivialTarget: the jump target of a
// break that needs no scope restoration or finally handling.
func (g *Generator) trivialBreakTarget(n *parser.BreakNode) *Label {
	if g.shouldEmitDebugHooks() {
		return nil
	}
	scope := g.breakTarget(n.Ident)
	if g.labelScopeDepth() != scope.ScopeDepth() {
		return nil
	}
	return scope.BreakTarget()
}

func (g *Generator) trivialContinueTarget(n *parser.ContinueNode) *Label {
	if g.shouldEmitDebugHooks() {
		return nil
	}
	scope := g.continueTarget(n.Ident)
	if g.labelScopeDepth() != scope.ScopeDepth() {
		return nil
	}
	return scope.ContinueTarget()
}

// tryFoldBreakAndContinue turns `if (c) break;` into a conditional jump.
func (g *Generator) tryFoldBreakAndContinue(ifBlock parser.StatementNode) (*Label, bool) {
	var target *Label
	switch s := singleStatement(ifBlock).(type) {
	case *parser.BreakNode:
		target = g.trivialBreakTarget(s)
	case *parser.ContinueNode:
		target = g.trivialContinueTarget(s)
	}
	return target, target != nil
}

func (g *Generator) genIfElse(n *parser.IfElseNode, dst *RegisterID) {
	if g.shouldBeConcernedWithCompletionValue() {
		if parser.HasEarlyBreakOrContinue(n.IfBlock) || (n.ElseBlock != nil && parser.HasEarlyBreakOrContinue(n.ElseBlock)) {
			g.emitLoad(dst, bytecode.Undefined{})
		}
	}

	beforeThen := g.newLabel()
	beforeElse := g.newLabel()
	afterElse := g.newLabel()

	trueTarget := beforeThen
	fallThroughMode := FallThroughMeansTrue
	foldedTarget, didFoldIfBlock := g.tryFoldBreakAndContinue(n.IfBlock)
	if didFoldIfBlock {
		trueTarget = foldedTarget
		fallThroughMode = FallThroughMeansFalse
	}

	g.emitNodeInConditionContext(n.Condition, trueTarget, beforeElse, fallThroughMode)
	g.emitLabel(beforeThen)
	g.emitProfileControlFlow(n.IfBlock.BaseNode().StartOffset())

	if !didFoldIfBlock {
		g.emitStatementInTailPosition(dst, n.IfBlock)
		if n.ElseBlock != nil {
			g.emitJump(afterElse)
		}
	}

	g.emitLabel(beforeElse)

	if n.ElseBlock != nil {
		g.emitProfileControlFlow(blockEndOffset(n.IfBlock))
		g.emitStatementInTailPosition(dst, n.ElseBlock)
	}

	g.emitLabel(afterElse)
	endingBlock := n.IfBlock
	if n.ElseBlock != nil {
		endingBlock = n.ElseBlock
	}
	g.emitProfileControlFlow(blockEndOffset(endingBlock))
}

func (g *Generator) genDoWhile(n *parser.DoWhileNode, dst *RegisterID) {
	defer g.refScope()()
	if g.shouldBeConcernedWithCompletionValue() && parser.HasEarlyBreakOrContinue(n.Statement) {
		g.emitLoad(dst, bytecode.Undefined{})
	}

	scope := g.newLabelScope(LabelScopeLoop, ident.Null)

	topOfLoop := g.newLabel()
	g.emitLabel(topOfLoop)
	g.emitLoopHint()

	g.emitStatementInTailPosition(dst, n.Statement)

	g.emitLabel(scope.ContinueTarget())
	g.emitNodeInConditionContext(n.Expr, topOfLoop, scope.BreakTarget(), FallThroughMeansFalse)

	g.emitLabel(scope.BreakTarget())
}

func (g *Generator) genWhile(n *parser.WhileNode, dst *RegisterID) {
	defer g.refScope()()
	if g.shouldBeConcernedWithCompletionValue() && parser.HasEarlyBreakOrContinue(n.Statement) {
		g.emitLoad(dst, bytecode.Undefined{})
	}

	scope := g.newLabelScope(LabelScopeLoop, ident.Null)
	topOfLoop := g.newLabel()

	g.emitNodeInConditionContext(n.Expr, topOfLoop, scope.BreakTarget(), FallThroughMeansTrue)

	g.emitLabel(topOfLoop)
	g.emitLoopHint()

	g.emitProfileControlFlow(n.Statement.BaseNode().StartOffset())
	g.emitStatementInTailPosition(dst, n.Statement)

	g.emitLabel(scope.ContinueTarget())

	g.emitNodeInConditionContext(n.Expr, topOfLoop, scope.BreakTarget(), FallThroughMeansFalse)

	g.emitLabel(scope.BreakTarget())

	g.emitProfileControlFlow(blockEndOffset(n.Statement))
}

func (g *Generator) genFor(n *parser.ForNode, dst *RegisterID) {
	if g.shouldBeConcernedWithCompletionValue() && parser.HasEarlyBreakOrContinue(n.Statement) {
		g.emitLoad(dst, bytecode.Undefined{})
	}

	var forLoopSymbolTable *RegisterID
	g.pushLexicalScope(&n.VariableEnvironmentNode, ScopeTypeLetConstScope, TDZCheckOptimize, NestedScopeIsNested, &forLoopSymbolTable, true)

	g.emitBodyWithUsingIfNeeded(n.UsingDeclarationCount(), n.HasAwaitUsingDeclaration(), func(g *Generator) {
		defer g.refScope()()
		scope := g.newLabelScope(LabelScopeLoop, ident.Null)

		if n.Expr1 != nil {
			g.emitNodeInIgnoreResultPosition(n.Expr1)
			if n.InitializerContainsClosure {
				g.prepareLexicalScopeForNextForLoopIteration(&n.VariableEnvironmentNode, forLoopSymbolTable)
			}
		}

		topOfLoop := g.newLabel()
		if n.Expr2 != nil {
			g.emitNodeInConditionContext(n.Expr2, topOfLoop, scope.BreakTarget(), FallThroughMeansTrue)
		}

		g.emitLabel(topOfLoop)
		g.emitLoopHint()
		g.emitProfileControlFlow(n.Statement.BaseNode().StartOffset())

		g.emitStatementInTailPosition(dst, n.Statement)

		g.emitLabel(scope.ContinueTarget())
		g.prepareLexicalScopeForNextForLoopIteration(&n.VariableEnvironmentNode, forLoopSymbolTable)
		if n.Expr3 != nil {
			g.emitNodeInIgnoreResultPosition(n.Expr3)
		}

		if n.Expr2 != nil {
			g.emitNodeInConditionContext(n.Expr2, topOfLoop, scope.BreakTarget(), FallThroughMeansFalse)
		} else {
			g.emitJump(topOfLoop)
		}

		g.emitLabel(scope.BreakTarget())
	})

	g.popLexicalScope(&n.VariableEnvironmentNode)
	g.emitProfileControlFlow(blockEndOffset(n.Statement))
}

// --- for-in and for-of ---------------------------------------------------------------

// tryGetBoundLocal returns the local register a for-in loop assigns its key
// to, if it is a plain local.
func (g *Generator) tryGetBoundLocal(n *parser.ForInNode) *RegisterID {
	switch l := n.Lexpr.(type) {
	case *parser.ResolveNode:
		return g.variable(l.Ident, ThisResolutionLocal).Local()
	case *parser.DestructuringAssignmentNode:
		simpleBinding, ok := l.Bindings.(*parser.BindingNode)
		if !ok {
			return nil
		}
		v := g.variable(simpleBinding.BoundProperty, ThisResolutionLocal)
		if v.IsSpecial() {
			return nil
		}
		return v.Local()
	}
	return nil
}

// emitForInLoopHeader assigns the current key to the loop's left side.
func (g *Generator) emitForInLoopHeader(n *parser.ForInNode, propertyName *RegisterID) {
	defer g.refScope()()
	emitResolveVariable := func(id ident.Identifier) {
		v := g.variable(id, ThisResolutionLocal)
		if local := v.Local(); local != nil {
			if v.IsReadOnly() {
				g.emitReadOnlyExceptionIfNeeded(v)
			}
			g.move(local, propertyName)
		} else {
			if g.ecmaMode.IsStrict() {
				g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
			}
			if v.IsReadOnly() {
				g.emitReadOnlyExceptionIfNeeded(v)
			}
			scope := g.keep(g.emitResolveScope(nil, v))
			g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
			g.emitPutToScope(scope, v, propertyName, g.resolveModeForPut(), bytecode.InitializationModeNotInitialization)
		}
		position := n.Lexpr.BaseNode().Position()
		g.emitProfileTypeVariable(propertyName, v, position, position+identLength(id))
	}

	switch l := n.Lexpr.(type) {
	case *parser.ResolveNode:
		emitResolveVariable(l.Ident)

	case *parser.AssignResolveNode:
		emitResolveVariable(l.Ident)

	case *parser.DotAccessorNode:
		base := g.keep(g.emitNode(nil, l.Base))
		g.emitExpressionInfo(l.Divot, l.DivotStart, l.DivotEnd)
		var thisValue *RegisterID
		g.emitPutProperty(dotOf(l), base, propertyName, &thisValue)
		g.emitProfileTypeRange(propertyName, l.DivotStart, l.DivotEnd)

	case *parser.BracketAccessorNode:
		base := g.keep(g.emitNode(nil, l.Base))
		subscript := g.keep(g.emitNodeForProperty(nil, l.Subscript))
		g.emitExpressionInfo(l.Divot, l.DivotStart, l.DivotEnd)
		if isSuperNode(l.Base) {
			thisValue := g.keep(g.ensureThis())
			g.emitPutByValWithThis(base, thisValue, subscript, propertyName)
		} else {
			g.emitPutByVal(base, subscript, propertyName)
		}
		g.emitProfileTypeRange(propertyName, l.DivotStart, l.DivotEnd)

	case *parser.DestructuringAssignmentNode:
		simpleBinding, ok := l.Bindings.(*parser.BindingNode)
		if !ok {
			g.bindValue(l.Bindings, propertyName)
			return
		}
		v := g.variable(simpleBinding.BoundProperty, ThisResolutionLocal)
		if v.Local() == nil || v.IsSpecial() {
			g.bindValue(l.Bindings, propertyName)
			return
		}
		g.move(v.Local(), propertyName)
		g.emitProfileTypeVariable(propertyName, v, simpleBinding.DivotStart, simpleBinding.DivotEnd)

	default:
		panic("bytecompiler: unexpected for-in left side")
	}
}

// propertyNameEnumeratorInitMode is JSPropertyNameEnumerator::InitMode.
const propertyNameEnumeratorInitMode = 0

func (g *Generator) genForIn(n *parser.ForInNode, dst *RegisterID) {
	defer g.refScope()()
	if _, ok := n.Lexpr.(*parser.AssignResolveNode); !ok && !parser.IsAssignmentLocation(n.Lexpr) {
		g.emitNode(nil, n.Lexpr)
		g.emitThrowReferenceErrorAt(&n.ThrowableExpressionData, "Left side of for-in statement is not a reference.", nil)
		return
	}

	if g.shouldBeConcernedWithCompletionValue() && parser.HasEarlyBreakOrContinue(n.Statement) {
		g.emitLoad(dst, bytecode.Undefined{})
	}

	var forLoopSymbolTable *RegisterID
	g.pushLexicalScope(&n.VariableEnvironmentNode, ScopeTypeLetConstScope, TDZCheckOptimize, NestedScopeIsNested, &forLoopSymbolTable, true)

	if _, ok := n.Lexpr.(*parser.AssignResolveNode); ok {
		g.emitNodeInIgnoreResultPosition(n.Lexpr)
	}

	base := g.keep(g.newTemporary())

	g.emitNode(base, n.Expr)
	local := g.keep(g.tryGetBoundLocal(n))

	var baseVariable *Variable
	if v, ok := g.tryResolveVariable(n.Expr); ok {
		baseVariable = &v
	}

	profilerStartOffset := n.Statement.BaseNode().StartOffset()
	profilerEndOffset := blockEndOffset(n.Statement)

	g.scoped(func() {
		g.keep(g.newTemporary()) // the initial enumerator register
		mode := g.keep(g.emitLoad(g.newTemporary(), bytecode.JSInt32(propertyNameEnumeratorInitMode)))
		index := g.keep(g.emitLoad(g.newTemporary(), bytecode.JSInt32(0)))
		propertyName := g.keep(g.newTemporary())
		scope := g.newLabelScope(LabelScopeLoop, ident.Null)

		enumerator := g.keep(g.emitGetPropertyEnumerator(g.newTemporary(), base))
		g.emitJumpIfEmptyPropertyNameEnumerator(enumerator, scope.BreakTarget())

		g.emitLabel(scope.ContinueTarget())
		g.emitLoopHint()
		g.prepareLexicalScopeForNextForLoopIteration(&n.VariableEnvironmentNode, forLoopSymbolTable)
		g.emitDebugHook(n.Lexpr, nil) // Pause at the assignment expression for each for..in iteration.

		// FIXME: Skip the toString in op_enumerator_next if the property
		// name is only used for get_by_val.
		g.emitEnumeratorNext(propertyName, mode, index, base, enumerator)
		g.emitJumpIfSentinelString(propertyName, scope.BreakTarget())

		g.emitForInLoopHeader(n, propertyName)

		g.emitProfileControlFlow(profilerStartOffset)

		g.pushForInScope(local, propertyName, index, enumerator, mode, baseVariable)
		g.emitStatement(dst, n.Statement)
		g.popForInScope(local)

		g.emitProfileControlFlow(profilerEndOffset)
		g.emitJump(scope.ContinueTarget())

		g.emitLabel(scope.BreakTarget())
	})

	g.popLexicalScope(&n.VariableEnvironmentNode)
	g.emitProfileControlFlow(profilerEndOffset)
}

func (g *Generator) genForOf(n *parser.ForOfNode, dst *RegisterID) {
	if !parser.IsAssignmentLocation(n.Lexpr) {
		g.emitNode(nil, n.Lexpr)
		g.emitThrowReferenceErrorAt(&n.ThrowableExpressionData, "Left side of for-of statement is not a reference.", nil)
		return
	}

	if g.shouldBeConcernedWithCompletionValue() && parser.HasEarlyBreakOrContinue(n.Statement) {
		g.emitLoad(dst, bytecode.Undefined{})
	}

	var forLoopSymbolTable *RegisterID
	g.pushLexicalScope(&n.VariableEnvironmentNode, ScopeTypeLetConstScope, TDZCheckOptimize, NestedScopeIsNested, &forLoopSymbolTable, true)
	isUsingDeclaration := n.HasUsingDeclaration()
	isAwaitUsingDeclaration := n.HasAwaitUsingDeclaration()
	extractor := func(g *Generator, value *RegisterID) {
		emitBody := func(g *Generator) {
			defer g.refScope()()
			switch l := n.Lexpr.(type) {
			case *parser.ResolveNode:
				v := g.variable(l.Ident, ThisResolutionLocal)
				if local := v.Local(); local != nil {
					if v.IsReadOnly() && !isUsingDeclaration {
						g.emitReadOnlyExceptionIfNeeded(v)
					}
					g.move(local, value)
				} else {
					if g.ecmaMode.IsStrict() {
						g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
					}
					if v.IsReadOnly() && !isUsingDeclaration {
						g.emitReadOnlyExceptionIfNeeded(v)
					}
					scope := g.keep(g.emitResolveScope(nil, v))
					g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
					initMode := bytecode.InitializationModeNotInitialization
					if isUsingDeclaration {
						initMode = bytecode.InitializationModeConstInitialization
					}
					g.emitPutToScope(scope, v, value, g.resolveModeForPut(), initMode)
				}
				position := l.Position()
				g.emitProfileTypeVariable(value, v, position, position+identLength(l.Ident))
				if isUsingDeclaration {
					g.emitPrepareDisposable(value, n.DivotStart, isAwaitUsingDeclaration)
				}
			case *parser.DotAccessorNode:
				base := g.keep(g.emitNode(nil, l.Base))
				g.emitExpressionInfo(l.Divot, l.DivotStart, l.DivotEnd)
				var thisValue *RegisterID
				g.emitPutProperty(dotOf(l), base, value, &thisValue)
				g.emitProfileTypeRange(value, l.DivotStart, l.DivotEnd)
			case *parser.BracketAccessorNode:
				base := g.keep(g.emitNode(nil, l.Base))
				subscript := g.emitNodeForProperty(nil, l.Subscript)

				g.emitExpressionInfo(l.Divot, l.DivotStart, l.DivotEnd)
				if isSuperNode(l.Base) {
					thisValue := g.keep(g.ensureThis())
					g.emitPutByValWithThis(base, thisValue, subscript, value)
				} else {
					g.emitPutByVal(base, subscript, value)
				}
				g.emitProfileTypeRange(value, l.DivotStart, l.DivotEnd)
			case *parser.DestructuringAssignmentNode:
				g.bindValue(l.Bindings, value)
			}
			g.emitProfileControlFlow(n.Statement.BaseNode().StartOffset())
			g.emitStatement(dst, n.Statement)
		}

		usingCount := 0
		if isUsingDeclaration {
			usingCount = 1
		}
		g.emitBodyWithUsingIfNeeded(usingCount, isAwaitUsingDeclaration, emitBody)
	}
	g.emitEnumeration(&n.ThrowableExpressionData, n.Expr, extractor, n, forLoopSymbolTable)
	g.popLexicalScope(&n.VariableEnvironmentNode)
	g.emitProfileControlFlow(blockEndOffset(n.Statement))
}

// --- Jumps --------------------------------------------------------------------

func (g *Generator) genContinue(n *parser.ContinueNode) {
	scope := g.continueTarget(n.Ident)

	if !g.emitJumpViaFinallyIfNeeded(scope.ScopeDepth(), scope.ContinueTarget()) {
		lexicalScopeIndex := g.labelScopeDepthToLexicalScopeIndex(scope.ScopeDepth())
		g.restoreScopeRegisterTo(lexicalScopeIndex)
		g.emitJump(scope.ContinueTarget())
	}

	g.emitProfileControlFlow(n.EndOffset())
}

func (g *Generator) genBreak(n *parser.BreakNode) {
	scope := g.breakTarget(n.Ident)

	if !g.emitJumpViaFinallyIfNeeded(scope.ScopeDepth(), scope.BreakTarget()) {
		lexicalScopeIndex := g.labelScopeDepthToLexicalScopeIndex(scope.ScopeDepth())
		g.restoreScopeRegisterTo(lexicalScopeIndex)
		g.emitJump(scope.BreakTarget())
	}

	g.emitProfileControlFlow(n.EndOffset())
}

func (g *Generator) genReturn(n *parser.ReturnNode, dst *RegisterID) {
	defer g.refScope()()
	if dst == g.IgnoredResult() {
		dst = nil
	}

	var returnRegister *RegisterID
	if n.Value != nil {
		// Within a finally scope there must be no tail call: the finally
		// block runs before actually returning.
		if g.hasFinallyScopes() {
			returnRegister = g.keep(g.emitNode(dst, n.Value))
		} else {
			returnRegister = g.keep(g.emitNodeInTailPositionFromReturnNode(dst, n.Value))
		}
		if g.parseMode() == parser.AsyncGeneratorBodyMode {
			returnRegister = g.keep(g.emitAwait(g.newTemporary(), returnRegister, n.Position()))
		}
	} else {
		returnRegister = g.keep(g.emitLoad(dst, bytecode.Undefined{}))
	}

	g.emitProfileTypeFlagRange(returnRegister, bytecode.ProfileTypeBytecodeFunctionReturnStatement, n.DivotStart, n.DivotEnd)

	if !g.emitReturnViaFinallyIfNeeded(returnRegister) {
		g.emitWillLeaveCallFrameDebugHook()
		g.emitReturn(returnRegister)
	}

	g.emitProfileControlFlow(n.EndOffset())
	// A code block must end with a terminal opcode, even after an
	// op_profile_control_flow.
	if g.shouldEmitControlFlowProfilerHooks() {
		g.emitReturn(g.emitLoad(nil, bytecode.Undefined{}))
	}
}

func (g *Generator) genWith(n *parser.WithNode, dst *RegisterID) {
	defer g.refScope()()
	scope := g.keep(g.emitNode(nil, n.Expr))
	g.emitExpressionInfo(n.Divot, n.Divot-parser.TextPosition(n.ExpressionLength), n.Divot)
	g.emitPushWithScope(scope)
	if g.shouldBeConcernedWithCompletionValue() && parser.HasEarlyBreakOrContinue(n.Statement) {
		g.emitLoad(dst, bytecode.Undefined{})
	}
	g.emitStatementInTailPosition(dst, n.Statement)
	g.emitPopWithScope()
}

// --- switch -----------------------------------------------------------------------

func (g *Generator) emitCaseClause(c *parser.CaseClauseNode, dst *RegisterID) {
	g.emitProfileControlFlow(c.StartOffset)
	if c.Statements == nil {
		return
	}
	g.emitSourceElements(c.Statements, dst)
}

type switchKind uint8

const (
	switchUnset   switchKind = 0
	switchNumber  switchKind = 1
	switchString  switchKind = 2
	switchNeither switchKind = 3
)

const (
	tableSwitchMinimum             = 3
	switchJumpTableAmountThreshold = 15
)

func processClauseList(list []*parser.CaseClauseNode, literals *[]parser.ExpressionNode, typeForTable *switchKind, singleCharacterSwitch *bool, minNum, maxNum *int32) {
	for _, clause := range list {
		clauseExpression := clause.Expr
		*literals = append(*literals, clauseExpression)
		switch e := clauseExpression.(type) {
		case *parser.NumberNode:
			intVal := truncateDoubleToInt32(e.Value)
			if *typeForTable&^switchNumber != 0 || float64(intVal) != e.Value {
				*typeForTable = switchNeither
				return
			}
			*minNum = min(*minNum, intVal)
			*maxNum = max(*maxNum, intVal)
			*typeForTable = switchNumber
			continue
		case *parser.StringNode:
			if *typeForTable&^switchString != 0 {
				*typeForTable = switchNeither
				return
			}
			value := e.Value.UTF16()
			*singleCharacterSwitch = *singleCharacterSwitch && len(value) == 1
			if *singleCharacterSwitch {
				intVal := int32(value[0])
				*minNum = min(*minNum, intVal)
				*maxNum = max(*maxNum, intVal)
			}
			*typeForTable = switchString
			continue
		}
		*typeForTable = switchNeither
		return
	}
}

// truncateDoubleToInt32 is JavaScript's ToInt32 on a double.
func truncateDoubleToInt32(d float64) int32 {
	if math.IsNaN(d) || math.IsInf(d, 0) {
		return 0
	}
	return int32(uint32(int64(math.Mod(math.Trunc(d), 4294967296))))
}

func tryTableSwitch(b *parser.CaseBlockNode, literals *[]parser.ExpressionNode, minNum, maxNum *int32) switchType {
	typeForTable := switchUnset
	singleCharacterSwitch := true

	processClauseList(b.List1, literals, &typeForTable, &singleCharacterSwitch, minNum, maxNum)
	processClauseList(b.List2, literals, &typeForTable, &singleCharacterSwitch, minNum, maxNum)

	if len(*literals) < tableSwitchMinimum {
		return switchTypeNone
	}

	if typeForTable == switchUnset || typeForTable == switchNeither {
		return switchTypeNone
	}

	if typeForTable == switchNumber {
		if *minNum <= *maxNum {
			// In int64: JSC's int32 subtraction overflows (undefined in C++)
			// for cases from -2^31 to 2^31-1, which made a dense table of
			// 2^32 entries here.
			r := int64(*maxNum) - int64(*minNum)
			if r <= 1000 && int(r)/len(*literals) < switchJumpTableAmountThreshold {
				return switchTypeImmediate
			}
			return switchTypeImmediateList
		}
		return switchTypeNone
	}

	if singleCharacterSwitch && *minNum <= *maxNum {
		r := int64(*maxNum) - int64(*minNum)
		if r <= 1000 && int(r)/len(*literals) < switchJumpTableAmountThreshold {
			return switchTypeCharacter
		}
		return switchTypeCharacterList
	}

	return switchTypeString
}

func (g *Generator) emitCaseBlock(b *parser.CaseBlockNode, switchExpression, dst *RegisterID) {
	defer g.refScope()()
	var labelVector []*Label
	var literalVector []parser.ExpressionNode
	minNum := int32(math.MaxInt32)
	maxNum := int32(math.MinInt32)
	typ := tryTableSwitch(b, &literalVector, &minNum, &maxNum)

	defaultLabel := g.newLabel()
	if typ != switchTypeNone {
		// Prepare the various labels.
		for range literalVector {
			labelVector = append(labelVector, g.newLabel())
		}
		g.beginSwitch(switchExpression, typ)
	} else {
		// Set up jumps.
		for _, list := range [][]*parser.CaseClauseNode{b.List1, b.List2} {
			for _, clause := range list {
				g.scoped(func() {
					clauseVal := g.keep(g.emitNode(nil, clause.Expr))
					clauseLabel := g.newLabel()
					labelVector = append(labelVector, clauseLabel)
					g.emitJumpIfTrue(g.emitEqualityOp(bytecode.OpStricteqID, g.newTemporary(), clauseVal, switchExpression), clauseLabel)
				})
			}
		}
		g.emitJump(defaultLabel)
	}

	i := 0
	for _, clause := range b.List1 {
		g.emitLabel(labelVector[i])
		i++
		g.emitCaseClause(clause, dst)
	}

	if b.DefaultClause != nil {
		g.emitLabel(defaultLabel)
		g.emitCaseClause(b.DefaultClause, dst)
	}

	for _, clause := range b.List2 {
		g.emitLabel(labelVector[i])
		i++
		g.emitCaseClause(clause, dst)
	}
	if b.DefaultClause == nil {
		g.emitLabel(defaultLabel)
	}

	if typ != switchTypeNone {
		g.endSwitch(labelVector, literalVector, defaultLabel, minNum, maxNum)
	}
}

func (g *Generator) genSwitch(n *parser.SwitchNode, dst *RegisterID) {
	defer g.refScope()()
	if g.shouldBeConcernedWithCompletionValue() {
		g.emitLoad(dst, bytecode.Undefined{})
	}

	scope := g.newLabelScope(LabelScopeSwitch, ident.Null)

	r0 := g.keep(g.emitNode(nil, n.Expr))

	g.pushLexicalScope(&n.VariableEnvironmentNode, ScopeTypeLetConstScope, TDZCheckDoNotOptimize, NestedScopeIsNested, nil, true)

	g.emitBodyWithUsingIfNeeded(n.UsingDeclarationCount(), n.HasAwaitUsingDeclaration(), func(g *Generator) {
		g.emitCaseBlock(n.Block, r0, dst)
	})

	g.popLexicalScope(&n.VariableEnvironmentNode)

	g.emitLabel(scope.BreakTarget())
	g.emitProfileControlFlow(n.EndOffset())
}

func (g *Generator) genLabel(n *parser.LabelNode, dst *RegisterID) {
	defer g.refScope()()
	scope := g.newLabelScope(LabelScopeNamedLabel, n.Name)
	g.emitStatementInTailPosition(dst, n.Statement)

	g.emitLabel(scope.BreakTarget())
}

func (g *Generator) genThrow(n *parser.ThrowNode) {
	defer g.refScope()()
	expr := g.keep(g.emitNode(nil, n.Expr))
	g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
	g.emitThrow(expr)

	g.emitProfileControlFlow(n.EndOffset())
}

// --- try ---------------------------------------------------------------------------

func (g *Generator) genTry(n *parser.TryNode, dst *RegisterID) {
	defer g.refScope()()
	// The catch and finally blocks are labeled explicitly, so that the
	// optimizer knows they may be jumped to from anywhere.
	tryCatchDst := g.keep(dst)
	if g.shouldBeConcernedWithCompletionValue() {
		if n.FinallyBlock != nil {
			tryCatchDst = g.keep(g.newTemporary())
		}
		if n.FinallyBlock != nil || parser.HasEarlyBreakOrContinue(n.TryBlock) {
			g.emitLoad(tryCatchDst, bytecode.Undefined{})
		}
	}

	var catchLabel, catchEndLabel, finallyLabel, finallyEndLabel *Label
	var finallyContext *FinallyContext

	if n.FinallyBlock != nil {
		finallyLabel = g.newLabel()
		finallyEndLabel = g.newLabel()

		finallyContext = g.newFinallyContext(finallyLabel)
		g.pushFinallyControlFlowScope(finallyContext)
	}
	if n.CatchBlock != nil {
		catchLabel = g.newLabel()
		catchEndLabel = g.newLabel()
	}

	tryLabel := g.newEmittedLabel()
	tryHandlerLabel, tryHandlerType := finallyLabel, bytecode.HandlerFinally
	if n.CatchBlock != nil {
		tryHandlerLabel, tryHandlerType = catchLabel, bytecode.HandlerCatch
	}
	td := g.pushTry(tryLabel, tryHandlerLabel, tryHandlerType)
	var finallyTryData *tryData
	if n.CatchBlock == nil && n.FinallyBlock != nil {
		finallyTryData = td
	}

	localScopeCountBeforeTryBlock := g.localScopeCount
	scopeRegisterMayBeClobbered := func() bool { return g.localScopeCount > localScopeCountBeforeTryBlock }

	if tryCatchDst == g.IgnoredResult() {
		g.emitStatementInIgnoreResultPosition(n.TryBlock)
	} else {
		g.emitStatement(tryCatchDst, n.TryBlock)
	}

	if n.CatchBlock != nil {
		if n.FinallyBlock != nil {
			g.emitJump(finallyLabel)
		} else {
			g.emitJump(catchEndLabel)
		}
	}

	tryEndLabel := g.newEmittedLabel()
	g.popTry(td, tryEndLabel)

	if n.CatchBlock != nil {
		// Uncaught exception path: the catch block.
		g.emitLabel(catchLabel)
		thrownValueRegister := g.keep(g.newTemporary())
		var completionTypeRegister *RegisterID
		if n.FinallyBlock != nil {
			completionTypeRegister = finallyContext.completionTypeRegister
		}
		g.emitOutOfLineCatchHandler(thrownValueRegister, completionTypeRegister, td)
		if scopeRegisterMayBeClobbered() {
			g.restoreScopeRegister()
		}

		if n.FinallyBlock != nil {
			// The finally block "catches" exceptions thrown by the catch block.
			finallyTryData = g.pushTry(catchLabel, finallyLabel, bytecode.HandlerFinally)
		}

		if n.CatchPattern != nil {
			scopeType := ScopeTypeCatchScope
			if _, ok := n.CatchPattern.(*parser.BindingNode); ok {
				scopeType = ScopeTypeCatchScopeWithSimpleParameter
			}
			g.emitPushCatchScope(&n.LexicalVariables, scopeType)
			g.bindValue(n.CatchPattern, thrownValueRegister)
		}

		g.emitProfileControlFlow(n.TryBlock.BaseNode().EndOffset() + 1)

		if g.shouldBeConcernedWithCompletionValue() {
			g.emitLoad(tryCatchDst, bytecode.Undefined{})
		}

		if n.FinallyBlock != nil {
			if tryCatchDst == g.IgnoredResult() {
				g.emitStatementInIgnoreResultPosition(n.CatchBlock)
			} else {
				g.emitStatement(tryCatchDst, n.CatchBlock)
			}
		} else {
			g.emitStatementInTailPosition(tryCatchDst, n.CatchBlock)
		}

		if n.CatchPattern != nil {
			g.emitPopCatchScope(&n.LexicalVariables)
		}

		if n.FinallyBlock != nil {
			g.emitLoadCompletionType(finallyContext.completionTypeRegister, CompletionNormal)
			g.popTry(finallyTryData, finallyLabel)
		}

		g.emitLabel(catchEndLabel)
		g.emitProfileControlFlow(n.CatchBlock.BaseNode().EndOffset() + 1)
	}

	if n.FinallyBlock != nil {
		g.popFinallyControlFlowScope()

		// The finally entry for Throw completions, generated later.
		g.emitOutOfLineFinallyHandler(finallyContext.completionValueRegister, finallyContext.completionTypeRegister, finallyTryData)

		// The finally entry for the other completion types.
		g.emitLabel(finallyLabel)
		if scopeRegisterMayBeClobbered() {
			g.restoreScopeRegister()
		}

		finallyStartOffset := n.TryBlock.BaseNode().EndOffset() + 1
		if n.CatchBlock != nil {
			finallyStartOffset = n.CatchBlock.BaseNode().EndOffset() + 1
		}

		// A finally block's completion value is ignored only for normal
		// completions.
		if g.shouldBeConcernedWithCompletionValue() {
			if parser.HasEarlyBreakOrContinue(n.FinallyBlock) {
				g.emitLoad(dst, bytecode.Undefined{})
			}

			g.emitProfileControlFlow(finallyStartOffset)
			g.emitStatementInTailPosition(dst, n.FinallyBlock)

			g.move(dst, tryCatchDst)
		} else {
			g.emitProfileControlFlow(finallyStartOffset)
			g.emitStatementInTailPosition(nil, n.FinallyBlock)
		}

		g.emitFinallyCompletion(finallyContext, finallyEndLabel)
		g.emitLabel(finallyEndLabel)
		g.emitProfileControlFlow(n.FinallyBlock.BaseNode().EndOffset() + 1)
	}
}
