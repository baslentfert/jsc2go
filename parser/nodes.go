package parser

import (
	"github.com/baslentfert/jsc2go/ident"
)

// Port of JavaScriptCore/parser/Nodes.h, NodeConstructors.h and Nodes.cpp
// (the parts that do not generate bytecode).
//
// Differences from C++:
//   - The isXxx() virtuals become type switches (see the predicates at the
//     bottom of this file).
//   - Families of classes that differ only in an opcode are folded into one
//     struct with a kind field (BinaryOpNode, UnaryOpNode, FunctionCallDotNode).
//   - Intrusive linked lists (StatementNode::m_next, ElementNode, PropertyListNode,
//     ArgumentListNode, ClauseListNode, CommaNode, template lists) are slices.

// Operator is the operator of an assignment or update expression.
type Operator uint8

const (
	OpEqual Operator = iota
	OpPlusEq
	OpMinusEq
	OpMultEq
	OpDivEq
	OpPlusPlus
	OpMinusMinus
	OpBitAndEq
	OpBitXOrEq
	OpBitOrEq
	OpModEq
	OpPowEq
	OpCoalesceEq
	OpOrEq
	OpAndEq
	OpLShift
	OpRShift
	OpURShift
)

type LogicalOperator uint8

const (
	LogicalAnd LogicalOperator = iota
	LogicalOr
)

type AssignmentContext uint8

const (
	AssignmentContextDeclarationStatement AssignmentContext = iota
	AssignmentContextConstDeclarationStatement
	AssignmentContextUsingDeclarationStatement
	AssignmentContextAwaitUsingDeclarationStatement
	AssignmentContextAssignmentExpression
)

// FunctionStack is the list of function declarations hoisted in a scope.
type FunctionStack []*FunctionMetadataNode

// --- Base types --------------------------------------------------------------

// Node is implemented by every AST node.
type Node interface {
	BaseNode() *NodeBase
}

// NodeBase holds what JSC's Node holds: a start position and an end offset.
type NodeBase struct {
	position       TextPosition
	endOffset      int
	needsDebugHook bool
}

func newNodeBase(loc TokenLocation) NodeBase {
	return NodeBase{position: TextPosition(loc.StartOffset), endOffset: -1}
}

func (n *NodeBase) BaseNode() *NodeBase       { return n }
func (n *NodeBase) Position() TextPosition    { return n.position }
func (n *NodeBase) StartOffset() int          { return int(n.position) }
func (n *NodeBase) EndOffset() int            { return n.endOffset }
func (n *NodeBase) SetStartOffset(offset int) { n.position = TextPosition(offset) }
func (n *NodeBase) SetEndOffset(offset int)   { n.endOffset = offset }
func (n *NodeBase) NeedsDebugHook() bool      { return n.needsDebugHook }
func (n *NodeBase) SetNeedsDebugHook()        { n.needsDebugHook = true }

// ExpressionNode is implemented by every expression.
type ExpressionNode interface {
	Node
	TreeExpression
	ExprBase() *ExpressionBase
}

// ExpressionBase is embedded in every expression node.
type ExpressionBase struct {
	NodeBase
	resultType          ResultType
	isOptionalChainBase bool
	isParenthesized     bool
}

func newExpressionBase(loc TokenLocation, rt ResultType) ExpressionBase {
	return ExpressionBase{NodeBase: newNodeBase(loc), resultType: rt}
}

func newExpr(loc TokenLocation) ExpressionBase { return newExpressionBase(loc, UnknownType()) }

func (e *ExpressionBase) ExprBase() *ExpressionBase    { return e }
func (*ExpressionBase) treeExpression()                {}
func (e *ExpressionBase) ResultDescriptor() ResultType { return e.resultType }
func (e *ExpressionBase) IsOptionalChainBase() bool    { return e.isOptionalChainBase }
func (e *ExpressionBase) SetIsOptionalChainBase()      { e.isOptionalChainBase = true }
func (e *ExpressionBase) IsParenthesized() bool        { return e.isParenthesized }
func (e *ExpressionBase) SetIsParenthesized()          { e.isParenthesized = true }

// StatementNode is implemented by every statement.
type StatementNode interface {
	Node
	TreeStatement
	stmtBase() *StatementBase
}

// StatementBase is embedded in every statement node.
type StatementBase struct {
	NodeBase
}

func newStatementBase(loc TokenLocation) StatementBase {
	return StatementBase{NodeBase: newNodeBase(loc)}
}

func (s *StatementBase) stmtBase() *StatementBase { return s }
func (*StatementBase) treeStatement()             {}

// ThrowableExpressionData records the source range reported when the node throws.
type ThrowableExpressionData struct {
	Divot, DivotStart, DivotEnd TextPosition
}

func (t *ThrowableExpressionData) Throwable() *ThrowableExpressionData { return t }

func (t *ThrowableExpressionData) SetExceptionSourceCode(divot, divotStart, divotEnd TextPosition) {
	t.Divot, t.DivotStart, t.DivotEnd = divot, divotStart, divotEnd
}

type throwableNode interface {
	Throwable() *ThrowableExpressionData
}

// ThrowableSubExpressionData additionally points at a sub-expression ending
// before the divot (e.g. the callee of a call).
type ThrowableSubExpressionData struct {
	ThrowableExpressionData
	SubexpressionDivotOffset uint16
	SubexpressionEndOffset   uint16
}

func (t *ThrowableSubExpressionData) SetSubexpressionInfo(subexpressionDivot TextPosition, subexpressionOffset int) {
	// Overflow means we can't do this safely, so just point at the primary divot.
	if (int(t.Divot)-int(subexpressionDivot))&^0xFFFF != 0 {
		return
	}
	if (int(t.DivotEnd)-subexpressionOffset)&^0xFFFF != 0 {
		return
	}
	t.SubexpressionDivotOffset = uint16(int(t.Divot) - int(subexpressionDivot))
	t.SubexpressionEndOffset = uint16(int(t.DivotEnd) - subexpressionOffset)
}

func (t *ThrowableSubExpressionData) SubexpressionDivot() TextPosition {
	return t.Divot - TextPosition(t.SubexpressionDivotOffset)
}
func (t *ThrowableSubExpressionData) SubexpressionStart() TextPosition { return t.DivotStart }
func (t *ThrowableSubExpressionData) SubexpressionEnd() TextPosition {
	return t.DivotEnd - TextPosition(t.SubexpressionEndOffset)
}

// ThrowablePrefixedSubExpressionData points at a sub-expression after the divot.
type ThrowablePrefixedSubExpressionData struct {
	ThrowableExpressionData
	SubexpressionDivotOffset uint16
	SubexpressionStartOffset uint16
}

func (t *ThrowablePrefixedSubExpressionData) SetSubexpressionInfo(subexpressionDivot TextPosition, subexpressionOffset int) {
	if (int(subexpressionDivot)-int(t.Divot))&^0xFFFF != 0 {
		return
	}
	if (subexpressionOffset-int(t.DivotStart))&^0xFFFF != 0 {
		return
	}
	t.SubexpressionDivotOffset = uint16(int(subexpressionDivot) - int(t.Divot))
	t.SubexpressionStartOffset = uint16(subexpressionOffset - int(t.DivotStart))
}

func (t *ThrowablePrefixedSubExpressionData) SubexpressionDivot() TextPosition {
	return t.Divot + TextPosition(t.SubexpressionDivotOffset)
}
func (t *ThrowablePrefixedSubExpressionData) SubexpressionStart() TextPosition {
	return t.DivotStart + TextPosition(t.SubexpressionStartOffset)
}
func (t *ThrowablePrefixedSubExpressionData) SubexpressionEnd() TextPosition { return t.DivotEnd }

// VariableEnvironmentNode is embedded by nodes that introduce a lexical scope.
type VariableEnvironmentNode struct {
	LexicalVariables VariableEnvironment
	FunctionStack    FunctionStack
}

func (v *VariableEnvironmentNode) HasUsingDeclaration() bool {
	return v.LexicalVariables.HasUsingDeclaration()
}
func (v *VariableEnvironmentNode) HasAwaitUsingDeclaration() bool {
	return v.LexicalVariables.HasAwaitUsingDeclaration()
}
func (v *VariableEnvironmentNode) UsingDeclarationCount() int {
	return v.LexicalVariables.UsingDeclarationCount()
}

// --- Constants ----------------------------------------------------------------

type NullNode struct{ ExpressionBase }

type BooleanNode struct {
	ExpressionBase
	Value bool
}

// NumberNode is JSC's NumberNode/DoubleNode/IntegerNode. IsInteger records
// that the literal was written as an integer (42, not 42. or 42.0).
type NumberNode struct {
	ExpressionBase
	Value     float64
	IsInteger bool
}

func newNumberNode(loc TokenLocation, v float64, isInteger bool) *NumberNode {
	rt := NumberType()
	if isInt32Value(v) {
		rt = NumberTypeIsInt32()
	}
	return &NumberNode{ExpressionBase: newExpressionBase(loc, rt), Value: v, IsInteger: isInteger}
}

// isInt32Value matches JSValue(v).isInt32(): an int32 that is not -0.
func isInt32Value(v float64) bool {
	i := int32(v)
	return float64(i) == v && (i != 0 || 1/v > 0)
}

type StringNode struct {
	ExpressionBase
	Value ident.Identifier
}

type BigIntNode struct {
	ExpressionBase
	Value ident.Identifier
	Radix uint8
	Sign  bool // true means negative
}

// --- Templates ------------------------------------------------------------------

// TemplateStringNode is one chunk of a template literal. Cooked is null when
// the chunk contains an invalid escape (allowed in tagged templates).
type TemplateStringNode struct {
	ExpressionBase
	Cooked ident.Identifier
	Raw    ident.Identifier
}

type TemplateLiteralNode struct {
	ExpressionBase
	TemplateStrings     []*TemplateStringNode
	TemplateExpressions []ExpressionNode
}

type TaggedTemplateNode struct {
	ExpressionBase
	ThrowableExpressionData
	Tag             ExpressionNode
	TemplateLiteral *TemplateLiteralNode
}

type RegExpNode struct {
	ExpressionBase
	ThrowableExpressionData
	Pattern ident.Identifier
	Flags   ident.Identifier
}

// --- Primary expressions -----------------------------------------------------

type ThisNode struct{ ExpressionBase }
type SuperNode struct{ ExpressionBase }

type ImportNode struct {
	ExpressionBase
	ThrowableExpressionData
	Expr     ExpressionNode
	Option   ExpressionNode
	Deferred bool
}

type NewTargetNode struct{ ExpressionBase }

type ImportMetaNode struct {
	ExpressionBase
	Expr ExpressionNode
}

type ResolveNode struct {
	ExpressionBase
	Ident ident.Identifier
	Start TextPosition
}

func (r *ResolveNode) IsArguments() bool { return r.Ident == ident.Arguments }

// PrivateIdentifierNode is the left-hand side of `#x in obj`.
type PrivateIdentifierNode struct {
	ExpressionBase
	Ident ident.Identifier
}

// ElementNode is one array literal element, preceded by Elision holes.
type ElementNode struct {
	Elision int
	Value   ExpressionNode
}

// ElementList is the builder's handle on a growing list of array elements.
type ElementList struct{ Elements []*ElementNode }

type ArrayNode struct {
	ExpressionBase
	Elements []*ElementNode
	Elision  int // trailing holes
}

// --- Object literals ---------------------------------------------------------

type ClassElementTag uint8

const (
	ClassElementTagNo ClassElementTag = iota
	ClassElementTagInstance
	ClassElementTagStatic
)

// PropertyType is a bit set (PropertyNode::Type).
type PropertyType uint16

const (
	PropertyConstant      PropertyType = 1
	PropertyGetter        PropertyType = 2
	PropertySetter        PropertyType = 4
	PropertyComputed      PropertyType = 8
	PropertyShorthand     PropertyType = 16
	PropertySpread        PropertyType = 32
	PropertyPrivateField  PropertyType = 64
	PropertyPrivateMethod PropertyType = 128
	PropertyPrivateSetter PropertyType = 256
	PropertyPrivateGetter PropertyType = 512
	PropertyBlock         PropertyType = 1024
)

// PropertyNode is one object literal property or class element.
type PropertyNode struct {
	Name                    ident.Identifier // null when the name is computed or for spreads
	Expression              ExpressionNode   // computed name, if any
	Assign                  ExpressionNode
	Type                    PropertyType
	NeedsSuperBinding       bool
	ClassElementTag         ClassElementTag
	IsOverriddenByDuplicate bool
	// Position is the start of the property (the position of JSC's
	// PropertyListNode wrapping it).
	Position TextPosition
}

func newPropertyNode(name ident.Identifier, expr, assign ExpressionNode, t PropertyType, sb SuperBinding, tag ClassElementTag) *PropertyNode {
	return &PropertyNode{Name: name, Expression: expr, Assign: assign, Type: t, NeedsSuperBinding: sb == SuperBindingNeeded, ClassElementTag: tag}
}

func (p *PropertyNode) HasName() bool         { return !p.Name.IsNull() }
func (p *PropertyNode) IsClassProperty() bool { return p.ClassElementTag != ClassElementTagNo }
func (p *PropertyNode) IsStaticClassProperty() bool {
	return p.ClassElementTag == ClassElementTagStatic
}
func (p *PropertyNode) IsInstanceClassProperty() bool {
	return p.ClassElementTag == ClassElementTagInstance
}
func (p *PropertyNode) IsClassField() bool { return p.IsClassProperty() && !p.NeedsSuperBinding }
func (p *PropertyNode) IsInstanceClassField() bool {
	return p.IsInstanceClassProperty() && !p.NeedsSuperBinding
}
func (p *PropertyNode) IsStaticClassField() bool {
	return p.IsStaticClassProperty() && !p.NeedsSuperBinding
}
func (p *PropertyNode) IsStaticClassBlock() bool { return p.Type&PropertyBlock != 0 }
func (p *PropertyNode) IsStaticClassElement() bool {
	return p.IsStaticClassBlock() || p.IsStaticClassField()
}
func (p *PropertyNode) IsPrivate() bool {
	return p.Type&(PropertyPrivateField|PropertyPrivateMethod|PropertyPrivateGetter|PropertyPrivateSetter) != 0
}
func (p *PropertyNode) HasComputedName() bool      { return p.Expression != nil }
func (p *PropertyNode) IsComputedClassField() bool { return p.IsClassField() && p.HasComputedName() }

// IsUnderscoreProtoSetter reports whether this is `__proto__: value` in an
// object literal, which sets the prototype instead of defining a property.
func (p *PropertyNode) IsUnderscoreProtoSetter() bool {
	return isUnderscoreProtoSetter(p.Name, p.Type, p.NeedsSuperBinding, p.IsClassProperty())
}

func isUnderscoreProtoSetter(name ident.Identifier, t PropertyType, needsSuperBinding, isClassProperty bool) bool {
	return name == ident.UnderscoreProto && t == PropertyConstant && !needsSuperBinding && !isClassProperty
}

type PropertyListNode struct {
	ExpressionBase
	Properties          []*PropertyNode
	HasPrivateAccessors bool
}

func (l *PropertyListNode) HasStaticallyNamedProperty(name ident.Identifier) bool {
	for _, p := range l.Properties {
		if p.IsStaticClassProperty() && p.HasName() && p.Name == name {
			return true
		}
	}
	return false
}

func (l *PropertyListNode) HasInstanceFields() bool {
	for _, p := range l.Properties {
		if p.IsInstanceClassField() {
			return true
		}
	}
	return false
}

// ShouldCreateLexicalScopeForClass: computed fields and private names need a
// class scope to hold their hidden bindings.
func ShouldCreateLexicalScopeForClass(l *PropertyListNode) bool {
	if l == nil {
		return false
	}
	for _, p := range l.Properties {
		if p.IsComputedClassField() || p.IsPrivate() {
			return true
		}
	}
	return false
}

type ObjectLiteralNode struct {
	ExpressionBase
	List *PropertyListNode // nil for {}
}

// --- Member access and calls -------------------------------------------------

type BracketAccessorNode struct {
	ExpressionBase
	ThrowableExpressionData
	Base                    ExpressionNode
	Subscript               ExpressionNode
	SubscriptHasAssignments bool
}

type DotType uint8

const (
	DotTypeName DotType = iota
	DotTypePrivateMember
)

// DotAccessorNode is base.ident or base.#ident.
type DotAccessorNode struct {
	ExpressionBase
	ThrowableExpressionData
	Base  ExpressionNode
	Ident ident.Identifier
	Type  DotType
}

func (d *DotAccessorNode) IsPrivateMember() bool { return d.Type == DotTypePrivateMember }

type SpreadExpressionNode struct {
	ExpressionBase
	ThrowableExpressionData
	Expression ExpressionNode
}

type ObjectSpreadExpressionNode struct {
	ExpressionBase
	ThrowableExpressionData
	Expression ExpressionNode
}

// ArgumentList is the builder's handle on a growing argument list.
type ArgumentList struct{ Args []ExpressionNode }

// ArgumentsNode is a call's argument list.
type ArgumentsNode struct {
	Args           []ExpressionNode
	HasAssignments bool
}

type NewExprNode struct {
	ExpressionBase
	ThrowableExpressionData
	Expr ExpressionNode
	Args *ArgumentsNode // nil for `new F` without parentheses
}

type EvalFunctionCallNode struct {
	ExpressionBase
	ThrowableExpressionData
	Args *ArgumentsNode
}

type FunctionCallValueNode struct {
	ExpressionBase
	ThrowableExpressionData
	Expr           ExpressionNode
	Args           *ArgumentsNode
	IsOptionalCall bool
}

type StaticBlockFunctionCallNode struct {
	ExpressionBase
	ThrowableExpressionData
	Expr ExpressionNode
}

type FunctionCallResolveNode struct {
	ExpressionBase
	ThrowableExpressionData
	Ident          ident.Identifier
	Args           *ArgumentsNode
	IsOptionalCall bool
}

type FunctionCallBracketNode struct {
	ExpressionBase
	ThrowableSubExpressionData
	Base                    ExpressionNode
	Subscript               ExpressionNode
	Args                    *ArgumentsNode
	SubscriptHasAssignments bool
	IsOptionalCall          bool
}

// CallDotKind selects the specialized FunctionCallDotNode subclasses.
type CallDotKind uint8

const (
	CallDotNormal           CallDotKind = iota // FunctionCallDotNode
	CallDotCall                                // CallFunctionCallDotNode: f.call(...)
	CallDotApply                               // ApplyFunctionCallDotNode: f.apply(...)
	CallDotHasOwnProperty                      // HasOwnPropertyFunctionCallDotNode
	CallDotReflectConstruct                    // ReflectConstructFunctionCallDotNode
)

type FunctionCallDotNode struct {
	ExpressionBase
	ThrowableSubExpressionData
	Kind                           CallDotKind
	Base                           ExpressionNode
	Ident                          ident.Identifier
	Type                           DotType
	Args                           *ArgumentsNode
	IsOptionalCall                 bool
	DistanceToInnermostCallOrApply int
}

// --- Unary operators -------------------------------------------------------------

type DeleteResolveNode struct {
	ExpressionBase
	ThrowableExpressionData
	Ident ident.Identifier
}

type DeleteBracketNode struct {
	ExpressionBase
	ThrowableExpressionData
	Base      ExpressionNode
	Subscript ExpressionNode
}

type DeleteDotNode struct {
	ExpressionBase
	ThrowableExpressionData
	Base  ExpressionNode
	Ident ident.Identifier
}

type DeleteValueNode struct {
	ExpressionBase
	Expr ExpressionNode
}

type VoidNode struct {
	ExpressionBase
	Expr ExpressionNode
}

type TypeOfResolveNode struct {
	ExpressionBase
	ThrowableExpressionData
	Ident ident.Identifier
}

type TypeOfValueNode struct {
	ExpressionBase
	Expr ExpressionNode
}

// PrefixNode is ++x / --x.
type PrefixNode struct {
	ExpressionBase
	ThrowablePrefixedSubExpressionData
	Expr     ExpressionNode
	Operator Operator
}

// PostfixNode is x++ / x--.
type PostfixNode struct {
	ExpressionBase
	ThrowablePrefixedSubExpressionData
	Expr     ExpressionNode
	Operator Operator
}

// UnaryOpKind selects among JSC's UnaryOpNode subclasses.
type UnaryOpKind uint8

const (
	UnaryPlus  UnaryOpKind = iota // UnaryPlusNode, op_to_number
	Negate                        // NegateNode, op_negate
	BitwiseNot                    // BitwiseNotNode, op_bitnot
	LogicalNot                    // LogicalNotNode, op_not
)

type UnaryOpNode struct {
	ExpressionBase
	Op   UnaryOpKind
	Expr ExpressionNode
}

// --- Binary operators ------------------------------------------------------------

// BinaryOpKind selects among JSC's BinaryOpNode subclasses.
type BinaryOpKind uint8

const (
	BinPow BinaryOpKind = iota
	BinMult
	BinDiv
	BinMod
	BinAdd
	BinSub
	BinLeftShift
	BinRightShift
	BinUnsignedRightShift
	BinLess
	BinGreater
	BinLessEq
	BinGreaterEq
	BinInstanceOf // ThrowableBinaryOpNode
	BinIn         // ThrowableBinaryOpNode
	BinEqual
	BinNotEqual
	BinStrictEqual
	BinNotStrictEqual
	BinBitAnd
	BinBitOr
	BinBitXOr
)

type BinaryOpNode struct {
	ExpressionBase
	ThrowableExpressionData // only meaningful for BinInstanceOf and BinIn
	Op                      BinaryOpKind
	Expr1                   ExpressionNode
	Expr2                   ExpressionNode
	RightHasAssignments     bool
	ShouldToUnsignedResult  bool
}

func newBinaryOpNode(loc TokenLocation, op BinaryOpKind, e1, e2 ExpressionNode, rightHasAssignments bool) *BinaryOpNode {
	r1, r2 := ResultDescriptor(e1), ResultDescriptor(e2)
	var rt ResultType
	switch op {
	case BinPow, BinMult, BinDiv, BinMod, BinSub:
		rt = ResultTypeForNonAddArith(r1, r2)
	case BinAdd:
		rt = ResultTypeForAdd(r1, r2)
	case BinLeftShift, BinRightShift, BinBitAnd, BinBitOr, BinBitXOr:
		rt = ResultTypeForBitOp()
	case BinUnsignedRightShift:
		rt = NumberType()
	default:
		rt = BooleanType()
	}
	return &BinaryOpNode{
		ExpressionBase:         newExpressionBase(loc, rt),
		Op:                     op,
		Expr1:                  e1,
		Expr2:                  e2,
		RightHasAssignments:    rightHasAssignments,
		ShouldToUnsignedResult: true,
	}
}

// IsThrowable reports whether the node is a ThrowableBinaryOpNode in JSC.
func (b *BinaryOpNode) IsThrowable() bool { return b.Op == BinInstanceOf || b.Op == BinIn }

type LogicalOpNode struct {
	ExpressionBase
	Operator LogicalOperator
	Expr1    ExpressionNode
	Expr2    ExpressionNode
}

type CoalesceNode struct {
	ExpressionBase
	Expr1                    ExpressionNode
	Expr2                    ExpressionNode
	HasAbsorbedOptionalChain bool
}

type OptionalChainNode struct {
	ExpressionBase
	Expr        ExpressionNode
	IsOutermost bool
}

type ConditionalNode struct {
	ExpressionBase
	Logical ExpressionNode
	Expr1   ExpressionNode
	Expr2   ExpressionNode
}

// --- Assignments ---------------------------------------------------------------

type ReadModifyResolveNode struct {
	ExpressionBase
	ThrowableExpressionData
	Ident               ident.Identifier
	Right               ExpressionNode
	Operator            Operator
	RightHasAssignments bool
}

type ShortCircuitReadModifyResolveNode struct {
	ExpressionBase
	ThrowableExpressionData
	Ident               ident.Identifier
	Right               ExpressionNode
	Operator            Operator
	RightHasAssignments bool
}

type AssignResolveNode struct {
	ExpressionBase
	ThrowableExpressionData
	Ident             ident.Identifier
	Right             ExpressionNode
	AssignmentContext AssignmentContext
}

type ReadModifyBracketNode struct {
	ExpressionBase
	ThrowableSubExpressionData
	Base                    ExpressionNode
	Subscript               ExpressionNode
	Right                   ExpressionNode
	Operator                Operator
	SubscriptHasAssignments bool
	RightHasAssignments     bool
}

type ShortCircuitReadModifyBracketNode struct {
	ExpressionBase
	ThrowableSubExpressionData
	Base                    ExpressionNode
	Subscript               ExpressionNode
	Right                   ExpressionNode
	Operator                Operator
	SubscriptHasAssignments bool
	RightHasAssignments     bool
}

type AssignBracketNode struct {
	ExpressionBase
	ThrowableExpressionData
	Base                    ExpressionNode
	Subscript               ExpressionNode
	Right                   ExpressionNode
	SubscriptHasAssignments bool
	RightHasAssignments     bool
}

type AssignDotNode struct {
	ExpressionBase
	ThrowableExpressionData
	Base                ExpressionNode
	Ident               ident.Identifier
	Type                DotType
	Right               ExpressionNode
	RightHasAssignments bool
}

type ReadModifyDotNode struct {
	ExpressionBase
	ThrowableSubExpressionData
	Base                ExpressionNode
	Ident               ident.Identifier
	Type                DotType
	Right               ExpressionNode
	Operator            Operator
	RightHasAssignments bool
}

type ShortCircuitReadModifyDotNode struct {
	ExpressionBase
	ThrowableSubExpressionData
	Base                ExpressionNode
	Ident               ident.Identifier
	Type                DotType
	Right               ExpressionNode
	Operator            Operator
	RightHasAssignments bool
}

// AssignErrorNode is an assignment to a call expression (sloppy mode web
// compatibility); it throws a ReferenceError at runtime.
type AssignErrorNode struct {
	ExpressionBase
	ThrowableExpressionData
	Left ExpressionNode
}

type CommaNode struct {
	ExpressionBase
	Exprs []ExpressionNode
}

type EmptyVarExpression struct {
	ExpressionBase
	Ident ident.Identifier
}

type EmptyLetExpression struct {
	ExpressionBase
	Ident ident.Identifier
}

// --- Functions and classes ------------------------------------------------------

// BaseFuncExprNode is implemented by function, arrow function and method expressions.
type BaseFuncExprNode interface {
	ExpressionNode
	Metadata() *FunctionMetadataNode
}

type funcExprBase struct {
	ExpressionBase
	metadata *FunctionMetadataNode
}

func (f *funcExprBase) Metadata() *FunctionMetadataNode { return f.metadata }

func newFuncExprBase(loc TokenLocation, name ident.Identifier, metadata *FunctionMetadataNode, source SourceCode, mode FunctionMode) funcExprBase {
	metadata.FinishParsing(source, name, mode)
	return funcExprBase{ExpressionBase: newExpr(loc), metadata: metadata}
}

type FuncExprNode struct{ funcExprBase }
type ArrowFuncExprNode struct{ funcExprBase }
type MethodDefinitionNode struct{ funcExprBase }

type YieldExprNode struct {
	ExpressionBase
	ThrowableExpressionData
	Argument ExpressionNode
	Delegate bool
}

type AwaitExprNode struct {
	ExpressionBase
	ThrowableExpressionData
	Argument ExpressionNode
}

type ClassExprNode struct {
	ExpressionBase
	ThrowableExpressionData
	VariableEnvironmentNode
	ClassHeadEnvironment  VariableEnvironment
	ClassSource           SourceCode
	Name                  ident.Identifier
	ecmaName              ident.Identifier
	ConstructorExpression ExpressionNode
	ClassHeritage         ExpressionNode
	ClassElements         *PropertyListNode
	NeedsLexicalScope     bool
}

func (c *ClassExprNode) EcmaName() ident.Identifier { return c.ecmaName }

// SetEcmaName records the inferred name (e.g. from `let x = class {}`); an
// explicitly named class keeps its own name.
func (c *ClassExprNode) SetEcmaName(name ident.Identifier) {
	if c.Name.IsNull() {
		c.ecmaName = name
	} else {
		c.ecmaName = c.Name
	}
}

func (c *ClassExprNode) HasStaticProperty(name ident.Identifier) bool {
	return c.ClassElements != nil && c.ClassElements.HasStaticallyNamedProperty(name)
}

func (c *ClassExprNode) HasInstanceFields() bool {
	return c.ClassElements != nil && c.ClassElements.HasInstanceFields()
}

type DestructuringAssignmentNode struct {
	ExpressionBase
	Bindings    DestructuringPatternNode
	Initializer ExpressionNode // nil for for-in/of heads
}

// FunctionMetadataNode describes a function without its body; the body is
// reparsed lazily when the function is first compiled.
type FunctionMetadataNode struct {
	NodeBase
	ImplementationVisibility      ImplementationVisibility
	LexicallyScopedFeatures       LexicallyScopedFeatures
	SuperBinding                  SuperBinding
	ConstructorKind               ConstructorKind
	NeedsClassFieldInitializer    bool
	IsArrowFunctionBodyExpression bool
	IsSloppyModeHoistedFunction   bool
	PrivateBrandRequirement       PrivateBrandRequirement
	ParseMode                     SourceParseMode
	FunctionMode                  FunctionMode
	Ident                         ident.Identifier
	ecmaName                      ident.Identifier
	FunctionStart                 int
	FunctionNameStart             int
	ParametersStart               int
	Source                        SourceCode
	ClassSource                   SourceCode
	StartStartOffset              int
	ParameterCount                int
}

func (*FunctionMetadataNode) treeFunctionBody() {}

func (f *FunctionMetadataNode) FinishParsing(source SourceCode, name ident.Identifier, mode FunctionMode) {
	f.Source = source
	f.Ident = name
	f.FunctionMode = mode
}

func (f *FunctionMetadataNode) SetEcmaName(name ident.Identifier) { f.ecmaName = name }

// EcmaName is the function's own name if it has one, else the inferred name.
func (f *FunctionMetadataNode) EcmaName() ident.Identifier {
	if f.Ident.Len() == 0 {
		return f.ecmaName
	}
	return f.Ident
}

func (f *FunctionMetadataNode) SetIsSloppyModeHoistedFunction() { f.IsSloppyModeHoistedFunction = true }

// FunctionParameter is one formal parameter and its default value.
type FunctionParameter struct {
	Pattern      DestructuringPatternNode
	DefaultValue ExpressionNode
}

type FunctionParameters struct {
	Patterns              []FunctionParameter
	IsSimpleParameterList bool
}

func NewFunctionParameters() *FunctionParameters {
	return &FunctionParameters{IsSimpleParameterList: true}
}

func (*FunctionParameters) treeFormalParameterList() {}

func (f *FunctionParameters) Size() int { return len(f.Patterns) }

// Append implements IsSimpleParameterList: any default value, rest parameter
// or destructuring pattern makes the list non-simple.
func (f *FunctionParameters) Append(pattern DestructuringPatternNode, defaultValue ExpressionNode) {
	_, isBinding := pattern.(*BindingNode)
	f.IsSimpleParameterList = f.IsSimpleParameterList && defaultValue == nil && isBinding
	f.Patterns = append(f.Patterns, FunctionParameter{pattern, defaultValue})
}

// --- Destructuring patterns -------------------------------------------------------

// DestructuringPatternNode is implemented by binding and assignment patterns.
type DestructuringPatternNode interface {
	TreeDestructuringPattern
	CollectBoundIdentifiers(out []ident.Identifier) []ident.Identifier
}

type ArrayPatternBindingType uint8

const (
	ArrayPatternElision ArrayPatternBindingType = iota
	ArrayPatternElement
	ArrayPatternRestElement
)

type ArrayPatternEntry struct {
	BindingType  ArrayPatternBindingType
	Pattern      DestructuringPatternNode
	DefaultValue ExpressionNode
}

type ArrayPatternNode struct {
	ThrowableExpressionData
	TargetPatterns []ArrayPatternEntry
}

func (*ArrayPatternNode) treeDestructuringPattern() {}

func (a *ArrayPatternNode) CollectBoundIdentifiers(out []ident.Identifier) []ident.Identifier {
	for _, e := range a.TargetPatterns {
		if e.Pattern != nil {
			out = e.Pattern.CollectBoundIdentifiers(out)
		}
	}
	return out
}

type ObjectPatternBindingType uint8

const (
	ObjectPatternElement ObjectPatternBindingType = iota
	ObjectPatternRestElement
)

type ObjectPatternEntry struct {
	PropertyName       ident.Identifier // null for computed keys and rest elements
	PropertyExpression ExpressionNode
	WasString          bool
	Pattern            DestructuringPatternNode
	DefaultValue       ExpressionNode
	BindingType        ObjectPatternBindingType
}

type ObjectPatternNode struct {
	ThrowableExpressionData
	ContainsRestElement      bool
	ContainsComputedProperty bool
	TargetPatterns           []ObjectPatternEntry
}

func (*ObjectPatternNode) treeDestructuringPattern() {}

func (o *ObjectPatternNode) CollectBoundIdentifiers(out []ident.Identifier) []ident.Identifier {
	for _, e := range o.TargetPatterns {
		out = e.Pattern.CollectBoundIdentifiers(out)
	}
	return out
}

// BindingNode binds a single identifier.
type BindingNode struct {
	DivotStart     TextPosition
	DivotEnd       TextPosition
	BoundProperty  ident.Identifier
	BindingContext AssignmentContext
}

func (*BindingNode) treeDestructuringPattern() {}

func (b *BindingNode) CollectBoundIdentifiers(out []ident.Identifier) []ident.Identifier {
	return append(out, b.BoundProperty)
}

type RestParameterNode struct {
	Pattern             DestructuringPatternNode
	NumParametersToSkip int
}

func (*RestParameterNode) treeDestructuringPattern() {}

func (r *RestParameterNode) CollectBoundIdentifiers(out []ident.Identifier) []ident.Identifier {
	return r.Pattern.CollectBoundIdentifiers(out)
}

// AssignmentElementNode is an arbitrary assignment target inside a
// destructuring assignment expression, e.g. a.b in [a.b] = arr.
type AssignmentElementNode struct {
	DivotStart       TextPosition
	DivotEnd         TextPosition
	AssignmentTarget ExpressionNode
}

func (*AssignmentElementNode) treeDestructuringPattern() {}

func (a *AssignmentElementNode) CollectBoundIdentifiers(out []ident.Identifier) []ident.Identifier {
	return out
}

// --- Statements -------------------------------------------------------------------

// SourceElements is a statement list. Empty statements are dropped, as in JSC.
type SourceElements struct {
	Statements []StatementNode
}

func (*SourceElements) treeSourceElements() {}

func (s *SourceElements) Append(st StatementNode) {
	if _, isEmpty := st.(*EmptyStatementNode); isEmpty {
		return
	}
	s.Statements = append(s.Statements, st)
}

func (s *SourceElements) SingleStatement() StatementNode {
	if len(s.Statements) == 1 {
		return s.Statements[0]
	}
	return nil
}

func (s *SourceElements) LastStatement() StatementNode {
	if len(s.Statements) == 0 {
		return nil
	}
	return s.Statements[len(s.Statements)-1]
}

func (s *SourceElements) HasCompletionValue() bool {
	for _, st := range s.Statements {
		if HasCompletionValue(st) {
			return true
		}
	}
	return false
}

func (s *SourceElements) HasEarlyBreakOrContinue() bool {
	for _, st := range s.Statements {
		if HasEarlyBreakOrContinue(st) {
			return true
		}
		if HasCompletionValue(st) {
			return false
		}
	}
	return false
}

type BlockNode struct {
	StatementBase
	VariableEnvironmentNode
	Statements *SourceElements // nil for {}
}

type EmptyStatementNode struct{ StatementBase }
type DebuggerStatementNode struct{ StatementBase }

type ExprStatementNode struct {
	StatementBase
	Expr ExpressionNode
}

type DeclarationStatement struct {
	StatementBase
	Expr ExpressionNode
}

type IfElseNode struct {
	StatementBase
	Condition ExpressionNode
	IfBlock   StatementNode
	ElseBlock StatementNode
}

type DoWhileNode struct {
	StatementBase
	Statement StatementNode
	Expr      ExpressionNode
}

type WhileNode struct {
	StatementBase
	Expr      ExpressionNode
	Statement StatementNode
}

type ForNode struct {
	StatementBase
	VariableEnvironmentNode
	Expr1, Expr2, Expr3        ExpressionNode
	Statement                  StatementNode
	InitializerContainsClosure bool
}

// EnumerationNode holds what for-in and for-of loops share.
type EnumerationNode struct {
	StatementBase
	ThrowableExpressionData
	VariableEnvironmentNode
	Lexpr     ExpressionNode
	Expr      ExpressionNode
	Statement StatementNode
}

type ForInNode struct{ EnumerationNode }

type ForOfNode struct {
	EnumerationNode
	IsForAwait bool
}

type ContinueNode struct {
	StatementBase
	ThrowableExpressionData
	Ident ident.Identifier // null for an unlabeled continue
}

type BreakNode struct {
	StatementBase
	ThrowableExpressionData
	Ident ident.Identifier // null for an unlabeled break
}

type ReturnNode struct {
	StatementBase
	ThrowableExpressionData
	Value ExpressionNode
}

type WithNode struct {
	StatementBase
	Expr             ExpressionNode
	Statement        StatementNode
	Divot            TextPosition
	ExpressionLength uint32
}

type LabelNode struct {
	StatementBase
	ThrowableExpressionData
	Name      ident.Identifier
	Statement StatementNode
}

type ThrowNode struct {
	StatementBase
	ThrowableExpressionData
	Expr ExpressionNode
}

type TryNode struct {
	StatementBase
	VariableEnvironmentNode // the catch environment
	TryBlock                StatementNode
	CatchPattern            DestructuringPatternNode
	CatchBlock              StatementNode
	FinallyBlock            StatementNode
}

type FuncDeclNode struct {
	StatementBase
	Metadata *FunctionMetadataNode
}

type ClassDeclNode struct {
	StatementBase
	ClassDeclaration ExpressionNode
}

// DefineFieldType distinguishes how a class field's key is given.
type DefineFieldType uint8

const (
	DefineFieldName DefineFieldType = iota
	DefineFieldPrivateName
	DefineFieldComputedName
)

type DefineFieldNode struct {
	StatementBase
	Ident  ident.Identifier
	Assign ExpressionNode
	Type   DefineFieldType
}

type CaseClauseNode struct {
	Expr        ExpressionNode // nil for default
	Statements  *SourceElements
	StartOffset int
}

// ClauseList is the builder's handle on a growing list of case clauses.
type ClauseList struct{ Clauses []*CaseClauseNode }

type CaseBlockNode struct {
	List1         []*CaseClauseNode
	DefaultClause *CaseClauseNode
	List2         []*CaseClauseNode
}

type SwitchNode struct {
	StatementBase
	VariableEnvironmentNode
	Expr  ExpressionNode
	Block *CaseBlockNode
}

// --- Modules -----------------------------------------------------------------------

type ModuleNameNode struct {
	NodeBase
	ModuleName ident.Identifier
}

type ImportSpecifierNode struct {
	NodeBase
	ImportedName ident.Identifier
	LocalName    ident.Identifier
}

type ImportSpecifierListNode struct{ Specifiers []*ImportSpecifierNode }

type ImportAttribute struct{ Key, Value ident.Identifier }

type ImportAttributesListNode struct{ Attributes []ImportAttribute }

// ModuleDeclarationNode is implemented by import/export declarations.
type ModuleDeclarationNode interface {
	StatementNode
	isModuleDeclaration()
}

type moduleDeclarationBase struct{ StatementBase }

func (*moduleDeclarationBase) isModuleDeclaration() {}

type ImportType uint8

const (
	ImportTypeNormal ImportType = iota
	ImportTypeDeferred
)

type ImportDeclarationNode struct {
	moduleDeclarationBase
	Type           ImportType
	SpecifierList  *ImportSpecifierListNode
	ModuleName     *ModuleNameNode
	AttributesList *ImportAttributesListNode
}

type ExportAllDeclarationNode struct {
	moduleDeclarationBase
	ModuleName     *ModuleNameNode
	AttributesList *ImportAttributesListNode
}

type ExportDefaultDeclarationNode struct {
	moduleDeclarationBase
	Declaration StatementNode
	LocalName   ident.Identifier
}

type ExportLocalDeclarationNode struct {
	moduleDeclarationBase
	Declaration StatementNode
}

type ExportSpecifierNode struct {
	NodeBase
	LocalName    ident.Identifier
	ExportedName ident.Identifier
}

type ExportSpecifierListNode struct{ Specifiers []*ExportSpecifierNode }

type ExportNamedDeclarationNode struct {
	moduleDeclarationBase
	SpecifierList  *ExportSpecifierListNode
	ModuleName     *ModuleNameNode // nil for `export { a, b }` without `from`
	AttributesList *ImportAttributesListNode
}

// ModuleScopeData tracks exported names and bindings of a module.
type ModuleScopeData struct {
	exportedNames    identSet
	exportedBindings orderedMap[[]ident.Identifier]
}

// ExportName records an exported name and reports whether it was new.
func (m *ModuleScopeData) ExportName(name ident.Identifier) bool { return m.exportedNames.add(name) }

// ExportBinding records that localName is exported as exportedName.
func (m *ModuleScopeData) ExportBinding(localName, exportedName ident.Identifier) {
	names, _ := m.exportedBindings.add(localName)
	*names = append(*names, exportedName)
}

// ExportedBindings iterates local names and the names they are exported as.
func (m *ModuleScopeData) ExportedBindings(yield func(ident.Identifier, []ident.Identifier) bool) {
	for i, k := range m.exportedBindings.keys {
		if !yield(k, m.exportedBindings.values[i]) {
			return
		}
	}
}

// --- Scope nodes -------------------------------------------------------------------

// ScopeNode is the root of a parse: a program, eval code, module or function.
type ScopeNode struct {
	StatementBase
	VariableEnvironmentNode
	StartStartOffset               int
	Features                       CodeFeatures
	LexicallyScopedFeatures        LexicallyScopedFeatures
	InnerArrowFunctionCodeFeatures InnerArrowFunctionCodeFeatures
	Source                         SourceCode
	VarDeclarations                VariableEnvironment
	NumConstants                   int
	Statements                     *SourceElements
}

// Scope returns the ScopeNode embedded in a parsed node.
func (s *ScopeNode) Scope() *ScopeNode { return s }

func (s *ScopeNode) UsesEval() bool { return s.Features&EvalFeature != 0 }
func (s *ScopeNode) HasShadowsArgumentsFeature() bool {
	return s.Features&ShadowsArgumentsFeature != 0
}
func (s *ScopeNode) UsesArguments() bool {
	return s.Features&ArgumentsFeature != 0 && s.Features&ShadowsArgumentsFeature == 0
}
func (s *ScopeNode) UsesArrowFunction() bool { return s.Features&ArrowFunctionFeature != 0 }
func (s *ScopeNode) IsStrictMode() bool {
	return s.LexicallyScopedFeatures&StrictModeLexicallyScopedFeature != 0
}
func (s *ScopeNode) UsesThis() bool          { return s.Features&ThisFeature != 0 }
func (s *ScopeNode) UsesSuperCall() bool     { return s.Features&SuperCallFeature != 0 }
func (s *ScopeNode) UsesSuperProperty() bool { return s.Features&SuperPropertyFeature != 0 }
func (s *ScopeNode) UsesNewTarget() bool     { return s.Features&NewTargetFeature != 0 }
func (s *ScopeNode) IsAsyncFunctionWithoutAwait() bool {
	return s.Features&AsyncFunctionWithoutAwaitFeature != 0
}
func (s *ScopeNode) HasCapturedVariables() bool { return s.VarDeclarations.HasCapturedVariables() }
func (s *ScopeNode) NeedsActivation() bool {
	return s.HasCapturedVariables() || s.Features&(EvalFeature|WithFeature) != 0
}
func (s *ScopeNode) Captures(id ident.Identifier) bool { return s.VarDeclarations.Captures(id) }
func (s *ScopeNode) UsesNonSimpleParameterList() bool {
	return s.Features&NonSimpleParameterListFeature != 0
}
func (s *ScopeNode) NeedsNewTargetRegisterForThisScope() bool {
	return s.UsesSuperCall() || s.UsesNewTarget()
}
func (s *ScopeNode) DoAnyInnerArrowFunctionsUseAnyFeature() bool {
	return s.InnerArrowFunctionCodeFeatures != NoInnerArrowFunctionFeatures
}
func (s *ScopeNode) DoAnyInnerArrowFunctionsUseArguments() bool {
	return s.InnerArrowFunctionCodeFeatures&ArgumentsInnerArrowFunctionFeature != 0
}
func (s *ScopeNode) DoAnyInnerArrowFunctionsUseSuperCall() bool {
	return s.InnerArrowFunctionCodeFeatures&SuperCallInnerArrowFunctionFeature != 0
}
func (s *ScopeNode) DoAnyInnerArrowFunctionsUseSuperProperty() bool {
	return s.InnerArrowFunctionCodeFeatures&SuperPropertyInnerArrowFunctionFeature != 0
}
func (s *ScopeNode) DoAnyInnerArrowFunctionsUseEval() bool {
	return s.InnerArrowFunctionCodeFeatures&EvalInnerArrowFunctionFeature != 0
}
func (s *ScopeNode) DoAnyInnerArrowFunctionsUseThis() bool {
	return s.InnerArrowFunctionCodeFeatures&ThisInnerArrowFunctionFeature != 0
}
func (s *ScopeNode) DoAnyInnerArrowFunctionsUseNewTarget() bool {
	return s.InnerArrowFunctionCodeFeatures&NewTargetInnerArrowFunctionFeature != 0
}

// NeededConstants allows for the extra jsUndefined()/jsNull() constants.
func (s *ScopeNode) NeededConstants() int { return s.NumConstants + 2 }

func (s *ScopeNode) IsEmptyBody() bool { return s.Statements == nil }

func (s *ScopeNode) SingleStatement() StatementNode {
	if s.Statements == nil {
		return nil
	}
	return s.Statements.SingleStatement()
}

// ParsedNode is implemented by the node types parse() can produce.
type ParsedNode interface {
	StatementNode
	Scope() *ScopeNode
}

type ProgramNode struct{ ScopeNode }
type EvalNode struct{ ScopeNode }

type ModuleProgramNode struct {
	ScopeNode
	UsesAwait       bool
	ModuleScopeData *ModuleScopeData
}

type FunctionNode struct {
	ScopeNode
	Ident        ident.Identifier
	FunctionMode FunctionMode
	Parameters   *FunctionParameters
}

func (f *FunctionNode) FinishParsing(name ident.Identifier, mode FunctionMode) {
	f.Ident = name
	f.FunctionMode = mode
}

// --- Predicates (JSC's virtual isXxx methods) ---------------------------------------

// ResultDescriptor returns the static result type of e.
func ResultDescriptor(e ExpressionNode) ResultType { return e.ExprBase().resultType }

func IsNumber(e ExpressionNode) bool { _, ok := e.(*NumberNode); return ok }
func IsString(e ExpressionNode) bool { _, ok := e.(*StringNode); return ok }
func IsBigInt(e ExpressionNode) bool { _, ok := e.(*BigIntNode); return ok }

// IsConstant reports whether e is a ConstantNode (null, boolean, number, string, bigint).
func IsConstant(e ExpressionNode) bool {
	switch e.(type) {
	case *NullNode, *BooleanNode, *NumberNode, *StringNode, *BigIntNode:
		return true
	}
	return false
}

func IsResolveNode(e ExpressionNode) bool { _, ok := e.(*ResolveNode); return ok }

// IsLocation reports whether e can be assigned to.
func IsLocation(e ExpressionNode) bool {
	switch e.(type) {
	case *ResolveNode, *BracketAccessorNode, *DotAccessorNode:
		return true
	}
	return false
}

func IsAssignmentLocation(e ExpressionNode) bool {
	if _, ok := e.(*DestructuringAssignmentNode); ok {
		return true
	}
	return IsLocation(e)
}

func IsPrivateLocation(e ExpressionNode) bool {
	d, ok := e.(*DotAccessorNode)
	return ok && d.Type == DotTypePrivateMember
}

func IsFunctionCall(e ExpressionNode) bool {
	switch e.(type) {
	case *EvalFunctionCallNode, *FunctionCallValueNode, *StaticBlockFunctionCallNode,
		*FunctionCallResolveNode, *FunctionCallBracketNode, *FunctionCallDotNode:
		return true
	}
	return false
}

func IsOptionalCall(e ExpressionNode) bool {
	switch n := e.(type) {
	case *FunctionCallValueNode:
		return n.IsOptionalCall
	case *FunctionCallResolveNode:
		return n.IsOptionalCall
	case *FunctionCallBracketNode:
		return n.IsOptionalCall
	case *FunctionCallDotNode:
		return n.IsOptionalCall
	}
	return false
}

func IsDeleteNode(e ExpressionNode) bool {
	switch e.(type) {
	case *DeleteResolveNode, *DeleteBracketNode, *DeleteDotNode, *DeleteValueNode:
		return true
	}
	return false
}

func IsMetaProperty(e ExpressionNode) bool {
	switch e.(type) {
	case *NewTargetNode, *ImportMetaNode:
		return true
	}
	return false
}

func IsObjectLiteral(e ExpressionNode) bool { _, ok := e.(*ObjectLiteralNode); return ok }
func IsArrayLiteral(e ExpressionNode) bool  { _, ok := e.(*ArrayNode); return ok }

// IsArgumentsLengthAccess matches `arguments.length`.
func IsArgumentsLengthAccess(e ExpressionNode) bool {
	d, ok := e.(*DotAccessorNode)
	if !ok || d.Ident != ident.Length {
		return false
	}
	r, ok := d.Base.(*ResolveNode)
	return ok && r.IsArguments()
}

// StripUnaryPlus returns x for +x and e otherwise.
func StripUnaryPlus(e ExpressionNode) ExpressionNode {
	if u, ok := e.(*UnaryOpNode); ok && u.Op == UnaryPlus {
		return u.Expr
	}
	return e
}

// HasCompletionValue reports whether a statement may produce a completion value.
func HasCompletionValue(s StatementNode) bool {
	switch n := s.(type) {
	case *EmptyStatementNode, *DebuggerStatementNode, *DeclarationStatement, *ContinueNode,
		*BreakNode, *FuncDeclNode, *ClassDeclNode:
		return false
	case *LabelNode:
		return HasCompletionValue(n.Statement)
	case *BlockNode:
		return n.Statements != nil && n.Statements.HasCompletionValue()
	case ModuleDeclarationNode:
		return false
	case ParsedNode:
		sn := n.Scope()
		return sn.Statements != nil && sn.Statements.HasCompletionValue()
	}
	return true
}

// HasEarlyBreakOrContinue reports whether a statement list may exit via
// break/continue before producing a completion value.
func HasEarlyBreakOrContinue(s StatementNode) bool {
	switch n := s.(type) {
	case *ContinueNode, *BreakNode:
		return true
	case *LabelNode:
		return HasEarlyBreakOrContinue(n.Statement)
	case *BlockNode:
		return n.Statements != nil && n.Statements.HasEarlyBreakOrContinue()
	case ParsedNode:
		sn := n.Scope()
		return sn.Statements != nil && sn.Statements.HasEarlyBreakOrContinue()
	}
	return false
}
