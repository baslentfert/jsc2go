package bytecompiler

import (
	"github.com/baslentfert/jsc2go/bytecode"
	"github.com/baslentfert/jsc2go/ident"
	"github.com/baslentfert/jsc2go/parser"
)

// Code generation for classes and module declarations (NodesCodegen.cpp,
// ClassExprNode through ExportNamedDeclarationNode).

func (g *Generator) genClassExpr(n *parser.ClassExprNode, dst *RegisterID) *RegisterID {
	defer g.refScope()()
	defer g.strictModeScope()()

	if !n.Name.IsNull() {
		g.pushClassHeadLexicalScope(&n.ClassHeadEnvironment)
	}

	// The class heritage is evaluated outside of the private names' scope.
	var superclass *RegisterID
	if n.ClassHeritage != nil {
		superclass = g.keep(g.newTemporary())
		g.emitNode(superclass, n.ClassHeritage)
	}

	if n.NeedsLexicalScope {
		g.pushLexicalScope(&n.VariableEnvironmentNode, ScopeTypeClassScope, TDZCheckOptimize, NestedScopeIsNested, nil, true)
	}

	hasPrivateNames := n.LexicalVariables.PrivateNamesSize() > 0
	shouldEmitPrivateBrand := n.LexicalVariables.HasInstancePrivateMethodOrAccessor()
	shouldInstallBrandOnConstructor := n.LexicalVariables.HasStaticPrivateMethodOrAccessor()
	if hasPrivateNames {
		g.pushPrivateAccessNames(n.LexicalVariables.PrivateNameEnvironment())
	}
	if shouldEmitPrivateBrand {
		g.emitCreatePrivateBrand(n.Position(), n.Position(), n.Position())
	}

	constructor := g.keep(g.tempDestination(dst))
	needsHomeObject := false

	needsClassFieldInitializer := n.HasInstanceFields()
	privateBrandRequirement := parser.PrivateBrandRequirementNone
	if shouldEmitPrivateBrand {
		privateBrandRequirement = parser.PrivateBrandRequirementNeeded
	}
	if n.ConstructorExpression != nil {
		metadata := n.ConstructorExpression.(parser.BaseFuncExprNode).Metadata()
		metadata.SetEcmaName(n.EcmaName())
		metadata.ClassSource = n.ClassSource
		metadata.NeedsClassFieldInitializer = needsClassFieldInitializer
		metadata.PrivateBrandRequirement = privateBrandRequirement
		constructor = g.keep(g.emitNode(constructor, n.ConstructorExpression))
		needsHomeObject = n.ClassHeritage != nil || metadata.SuperBinding == parser.SuperBindingNeeded
	} else {
		kind := parser.ConstructorKindBase
		if n.ClassHeritage != nil {
			kind = parser.ConstructorKindExtends
		}
		constructor = g.keep(g.emitNewDefaultConstructor(constructor, kind, n.Name, n.EcmaName(), n.ClassSource, needsClassFieldInitializer, privateBrandRequirement))
	}

	prototype := g.keep(g.emitNewObject(g.newTemporary()))

	if superclass != nil {
		protoParent := g.keep(g.newTemporary())
		g.emitLoad(protoParent, bytecode.Null{})

		superclassIsNullLabel := g.newLabel()
		g.emitJumpIfTrue(g.emitIsNull(g.newTemporary(), superclass), superclassIsNullLabel)

		superclassIsConstructorLabel := g.newLabel()
		g.emitJumpIfTrue(g.emitIsConstructor(g.newTemporary(), superclass), superclassIsConstructorLabel)
		g.emitExpressionInfo(n.Divot, n.DivotStart, n.DivotEnd)
		g.emitThrowTypeError("The superclass is not a constructor.")
		g.emitLabel(superclassIsConstructorLabel)
		g.emitGetByID(protoParent, superclass, ident.Prototype)

		g.emitDirectSetPrototypeOf(InvalidPrototypeThrow, constructor, superclass, n.Position(), n.Position(), n.Position()) // never actually throws
		g.emitLabel(superclassIsNullLabel)
		g.emitDirectSetPrototypeOf(InvalidPrototypeThrow, prototype, protoParent, n.Divot, n.DivotStart, n.DivotEnd)
	}

	if needsHomeObject {
		g.emitPutHomeObject(constructor, prototype)
	}

	constructorNameRegister := g.keep(g.emitLoadIdentifier(nil, ident.Constructor))
	g.emitCallDefineProperty(prototype, constructorNameRegister, constructor, nil, nil, PropertyConfigurable|PropertyWritable, n.Position())

	prototypeNameRegister := g.keep(g.emitLoadIdentifier(nil, ident.Prototype))
	g.emitCallDefineProperty(constructor, prototypeNameRegister, prototype, nil, nil, 0, n.Position())

	var staticElementDefinitions []parser.ClassElementDefinition
	if n.ClassElements != nil {
		g.emitDeclarePrivateFieldNames(n.ClassElements, g.scopeRegister)

		var instanceElementDefinitions []parser.ClassElementDefinition
		g.emitDefineClassElements(n.ClassElements, constructor, prototype, &instanceElementDefinitions, &staticElementDefinitions)
		if len(instanceElementDefinitions) > 0 {
			instanceFieldInitializer := g.keep(g.emitNewClassFieldInitializerFunction(g.newTemporary(), instanceElementDefinitions, n.ClassHeritage != nil))

			// FIXME: Skip the home object if the initializer doesn't need it
			// (no eval or super properties).
			g.emitPutHomeObject(instanceFieldInitializer, prototype)

			g.emitDirectPutByID(constructor, ident.InstanceFieldInitializerPrivateName, instanceFieldInitializer)
		}
	}

	if !n.Name.IsNull() {
		classNameVar := g.variable(n.Name, ThisResolutionLocal)
		scope := g.keep(g.emitResolveScope(nil, classNameVar))
		g.emitPutToScope(scope, classNameVar, constructor, bytecode.ThrowIfNotFound, bytecode.InitializationModeInitialization)
	}

	if shouldInstallBrandOnConstructor {
		g.emitInstallPrivateClassBrand(constructor)
	}

	if len(staticElementDefinitions) > 0 {
		staticFieldInitializer := g.keep(g.emitNewClassFieldInitializerFunction(g.newTemporary(), staticElementDefinitions, n.ClassHeritage != nil))
		// FIXME: Skip the home object if the initializer doesn't need it.
		g.emitPutHomeObject(staticFieldInitializer, constructor)

		args := g.newCallArguments(nil, 0)
		g.move(args.thisRegister(), constructor)
		g.emitCallIgnoreResult(g.newTemporary(), staticFieldInitializer, NoExpectedFunction, args, n.Position(), n.Position(), n.Position(), DebuggableCallNo)
	}

	if hasPrivateNames {
		g.popPrivateAccessNames()
	}

	if n.NeedsLexicalScope {
		g.popLexicalScope(&n.VariableEnvironmentNode)
	}

	if !n.Name.IsNull() {
		g.popClassHeadLexicalScope(&n.ClassHeadEnvironment)
	}

	return g.move(g.finalDestination(dst, constructor), constructor)
}

func (g *Generator) genExportDefaultDeclaration(n *parser.ExportDefaultDeclarationNode, dst *RegisterID) {
	g.emitStatement(dst, n.Declaration)
}

func (g *Generator) genExportLocalDeclaration(n *parser.ExportLocalDeclarationNode, dst *RegisterID) {
	g.emitStatement(dst, n.Declaration)
}
