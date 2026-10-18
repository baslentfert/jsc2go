package bytecompiler

import (
	"fmt"

	"github.com/baslentfert/jsc2go/parser"
)

// The AST lives in package parser, so the virtual emitBytecode methods of
// JSC's node classes become these type switches.

// emitExpr is ExpressionNode::emitBytecode.
func (g *Generator) emitExpr(n parser.ExpressionNode, dst *RegisterID) *RegisterID {
	switch n := n.(type) {
	case *parser.NullNode, *parser.BooleanNode, *parser.StringNode, *parser.BigIntNode:
		return g.genConstant(n, dst)
	case *parser.NumberNode:
		return g.genNumber(n, dst)
	case *parser.TemplateStringNode:
		return g.genTemplateString(n, dst)
	case *parser.TemplateLiteralNode:
		return g.genTemplateLiteral(n, dst)
	case *parser.TaggedTemplateNode:
		return g.genTaggedTemplate(n, dst)
	case *parser.RegExpNode:
		return g.genRegExp(n, dst)
	case *parser.ThisNode:
		return g.genThis(n, dst)
	case *parser.SuperNode:
		return g.genSuper(n, dst)
	case *parser.ImportNode:
		return g.genImport(n, dst)
	case *parser.NewTargetNode:
		return g.genNewTarget(n, dst)
	case *parser.ImportMetaNode:
		return g.genImportMeta(n, dst)
	case *parser.ResolveNode:
		return g.genResolve(n, dst)
	case *parser.ArrayNode:
		return g.genArray(n, dst)
	case *parser.ObjectLiteralNode:
		return g.genObjectLiteral(n, dst)
	case *parser.PropertyListNode:
		return g.emitPropertyList(n, n.Properties, dst, nil, nil, nil)
	case *parser.BracketAccessorNode:
		return g.genBracketAccessor(n, dst)
	case *parser.DotAccessorNode:
		return g.genDotAccessor(n, dst)
	case *parser.ObjectSpreadExpressionNode:
		return g.genObjectSpread(n, dst)
	case *parser.NewExprNode:
		return g.genNewExpr(n, dst)
	case *parser.EvalFunctionCallNode:
		return g.genEvalFunctionCall(n, dst)
	case *parser.FunctionCallValueNode:
		return g.genFunctionCallValue(n, dst)
	case *parser.StaticBlockFunctionCallNode:
		return g.genStaticBlockFunctionCall(n, dst)
	case *parser.FunctionCallResolveNode:
		return g.genFunctionCallResolve(n, dst)
	case *parser.FunctionCallBracketNode:
		return g.genFunctionCallBracket(n, dst)
	case *parser.FunctionCallDotNode:
		return g.genFunctionCallDot(n, dst)
	case *parser.DeleteResolveNode:
		return g.genDeleteResolve(n, dst)
	case *parser.DeleteBracketNode:
		return g.genDeleteBracket(n, dst)
	case *parser.DeleteDotNode:
		return g.genDeleteDot(n, dst)
	case *parser.DeleteValueNode:
		return g.genDeleteValue(n, dst)
	case *parser.VoidNode:
		return g.genVoid(n, dst)
	case *parser.TypeOfResolveNode:
		return g.genTypeOfResolve(n, dst)
	case *parser.TypeOfValueNode:
		return g.genTypeOfValue(n, dst)
	case *parser.PrefixNode:
		return g.genPrefix(n, dst)
	case *parser.PostfixNode:
		return g.genPostfix(n, dst)
	case *parser.UnaryOpNode:
		return g.genUnaryOp(n, dst)
	case *parser.BinaryOpNode:
		return g.genBinaryOp(n, dst)
	case *parser.LogicalOpNode:
		return g.genLogicalOp(n, dst)
	case *parser.CoalesceNode:
		return g.genCoalesce(n, dst)
	case *parser.OptionalChainNode:
		return g.genOptionalChain(n, dst)
	case *parser.ConditionalNode:
		return g.genConditional(n, dst)
	case *parser.ReadModifyResolveNode:
		return g.genReadModifyResolve(n, dst)
	case *parser.ShortCircuitReadModifyResolveNode:
		return g.genShortCircuitReadModifyResolve(n, dst)
	case *parser.AssignResolveNode:
		return g.genAssignResolve(n, dst)
	case *parser.ReadModifyBracketNode:
		return g.genReadModifyBracket(n, dst)
	case *parser.ShortCircuitReadModifyBracketNode:
		return g.genShortCircuitReadModifyBracket(n, dst)
	case *parser.AssignBracketNode:
		return g.genAssignBracket(n, dst)
	case *parser.AssignDotNode:
		return g.genAssignDot(n, dst)
	case *parser.ReadModifyDotNode:
		return g.genReadModifyDot(n, dst)
	case *parser.ShortCircuitReadModifyDotNode:
		return g.genShortCircuitReadModifyDot(n, dst)
	case *parser.AssignErrorNode:
		return g.genAssignError(n, dst)
	case *parser.CommaNode:
		return g.genComma(n, dst)
	case *parser.EmptyVarExpression:
		return g.genEmptyVarExpression(n)
	case *parser.EmptyLetExpression:
		return g.genEmptyLetExpression(n)
	case *parser.FuncExprNode:
		return g.genFuncExpr(n, dst)
	case *parser.ArrowFuncExprNode:
		return g.genFuncExpr(n, dst)
	case *parser.MethodDefinitionNode:
		return g.genFuncExpr(n, dst)
	case *parser.YieldExprNode:
		return g.genYield(n, dst)
	case *parser.AwaitExprNode:
		return g.genAwait(n, dst)
	case *parser.ClassExprNode:
		return g.genClassExpr(n, dst)
	case *parser.DestructuringAssignmentNode:
		return g.genDestructuringAssignment(n, dst)
	}
	// SpreadExpressionNode and PrivateIdentifierNode are handled by their
	// parents.
	panic(fmt.Sprintf("bytecompiler: no code generation for %T", n))
}

// emitStmt is StatementNode::emitBytecode.
func (g *Generator) emitStmt(n parser.StatementNode, dst *RegisterID) {
	switch n := n.(type) {
	case *parser.BlockNode:
		g.genBlock(n, dst)
	case *parser.EmptyStatementNode:
	case *parser.DebuggerStatementNode:
		g.genDebuggerStatement(n)
	case *parser.ExprStatementNode:
		g.genExprStatement(n, dst)
	case *parser.DeclarationStatement:
		g.genDeclarationStatement(n)
	case *parser.IfElseNode:
		g.genIfElse(n, dst)
	case *parser.DoWhileNode:
		g.genDoWhile(n, dst)
	case *parser.WhileNode:
		g.genWhile(n, dst)
	case *parser.ForNode:
		g.genFor(n, dst)
	case *parser.ForInNode:
		g.genForIn(n, dst)
	case *parser.ForOfNode:
		g.genForOf(n, dst)
	case *parser.ContinueNode:
		g.genContinue(n)
	case *parser.BreakNode:
		g.genBreak(n)
	case *parser.ReturnNode:
		g.genReturn(n, dst)
	case *parser.WithNode:
		g.genWith(n, dst)
	case *parser.LabelNode:
		g.genLabel(n, dst)
	case *parser.ThrowNode:
		g.genThrow(n)
	case *parser.TryNode:
		g.genTry(n, dst)
	case *parser.FuncDeclNode:
		g.genFuncDecl(n)
	case *parser.ClassDeclNode:
		g.genClassDecl(n)
	case *parser.DefineFieldNode:
		g.genDefineField(n)
	case *parser.SwitchNode:
		g.genSwitch(n, dst)
	case *parser.ImportDeclarationNode, *parser.ExportAllDeclarationNode, *parser.ExportNamedDeclarationNode:
		// Nothing happens at runtime.
	case *parser.ExportDefaultDeclarationNode:
		g.genExportDefaultDeclaration(n, dst)
	case *parser.ExportLocalDeclarationNode:
		g.genExportLocalDeclaration(n, dst)
	default:
		panic(fmt.Sprintf("bytecompiler: no code generation for %T", n))
	}
}

// emitBytecodeInConditionContext is
// ExpressionNode::emitBytecodeInConditionContext.
func (g *Generator) emitBytecodeInConditionContext(n parser.ExpressionNode, trueTarget, falseTarget *Label, fallThroughMode FallThroughMode) {
	switch n := n.(type) {
	case *parser.NullNode, *parser.BooleanNode, *parser.NumberNode, *parser.StringNode, *parser.BigIntNode:
		g.genConstantInConditionContext(n, trueTarget, falseTarget, fallThroughMode)
	case *parser.UnaryOpNode:
		if n.Op == parser.LogicalNot {
			g.genLogicalNotInConditionContext(n, trueTarget, falseTarget, fallThroughMode)
		} else {
			g.genExprInConditionContext(n, trueTarget, falseTarget, fallThroughMode)
		}
	case *parser.BinaryOpNode:
		g.genBinaryOpInConditionContext(n, trueTarget, falseTarget, fallThroughMode)
	case *parser.LogicalOpNode:
		g.genLogicalOpInConditionContext(n, trueTarget, falseTarget, fallThroughMode)
	case *parser.CoalesceNode:
		g.genCoalesceInConditionContext(n, trueTarget, falseTarget, fallThroughMode)
	case *parser.OptionalChainNode:
		g.genOptionalChainInConditionContext(n, trueTarget, falseTarget, fallThroughMode)
	case *parser.ConditionalNode:
		g.genConditionalInConditionContext(n, trueTarget, falseTarget, fallThroughMode)
	case *parser.CommaNode:
		g.genCommaInConditionContext(n, trueTarget, falseTarget, fallThroughMode)
	default:
		g.genExprInConditionContext(n, trueTarget, falseTarget, fallThroughMode)
	}
}
