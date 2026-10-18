package bytecode

import (
	"fmt"
	"slices"
)

// Port of JavaScriptCore/bytecode/BytecodeUseDef.{h,cpp}: which virtual
// registers an instruction reads (uses) and writes (defs) at a checkpoint.
//
// Most opcodes simply use or define some of their register operands; those
// are listed by operand name in the tables below and resolved to operand
// indices once. The remaining opcodes are handled in code.

// operandTable maps opcodes to the word indices of the listed operands.
type operandTable [NumberOfBytecodeIDs][]int

func buildOperandTable(spec map[OpcodeID][]string) *operandTable {
	var t operandTable
	for op, names := range spec {
		indices := []int{} // non-nil: "listed, possibly with no operands"
		for _, name := range names {
			i := slices.Index(op.OperandNames(), name)
			if i < 0 {
				panic(fmt.Sprintf("bytecode: %s has no operand %q", op, name))
			}
			indices = append(indices, i+1)
		}
		t[op] = indices
	}
	return &t
}

var simpleUses = buildOperandTable(map[OpcodeID][]string{
	// No uses.
	OpNewRegExpID: {}, OpLoopHintID: {}, OpJmpID: {}, OpNewObjectID: {}, OpNewPromiseID: {},
	OpNewGeneratorID: {}, OpNewAsyncFunctionGeneratorID: {}, OpEnterID: {}, OpArgumentCountID: {},
	OpCatchID: {}, OpProfileControlFlowID: {}, OpCreateDirectArgumentsID: {},
	OpCreateClonedArgumentsID: {}, OpCreateRestID: {}, OpCheckTrapsID: {}, OpGetArgumentID: {},
	OpNopID: {}, OpUnreachableID: {}, OpSuperSamplerBeginID: {}, OpSuperSamplerEndID: {},
	OpGetScopeID: {},

	OpToThisID:                          {"srcDst"},
	OpCheckTdzID:                        {"targetVirtualRegister"},
	OpIdentityWithProfileID:             {"srcDst"},
	OpProfileTypeID:                     {"targetVirtualRegister"},
	OpThrowID:                           {"value"},
	OpThrowStaticErrorID:                {"message"},
	OpDebugID:                           {"data"},
	OpRetID:                             {"value"},
	OpJtrueID:                           {"condition"},
	OpJfalseID:                          {"condition"},
	OpJeqNullID:                         {"value"},
	OpJneqNullID:                        {"value"},
	OpJundefinedOrNullID:                {"value"},
	OpJnundefinedOrNullID:               {"value"},
	OpDecID:                             {"srcDst"},
	OpIncID:                             {"srcDst"},
	OpLogShadowChickenPrologueID:        {"scope"},
	OpJlessID:                           {"lhs", "rhs"},
	OpJlesseqID:                         {"lhs", "rhs"},
	OpJgreaterID:                        {"lhs", "rhs"},
	OpJgreatereqID:                      {"lhs", "rhs"},
	OpJnlessID:                          {"lhs", "rhs"},
	OpJnlesseqID:                        {"lhs", "rhs"},
	OpJngreaterID:                       {"lhs", "rhs"},
	OpJngreatereqID:                     {"lhs", "rhs"},
	OpJeqID:                             {"lhs", "rhs"},
	OpJneqID:                            {"lhs", "rhs"},
	OpJstricteqID:                       {"lhs", "rhs"},
	OpJnstricteqID:                      {"lhs", "rhs"},
	OpJbelowID:                          {"lhs", "rhs"},
	OpJbeloweqID:                        {"lhs", "rhs"},
	OpJeqPtrID:                          {"value", "specialPointer"},
	OpJneqPtrID:                         {"value", "specialPointer"},
	OpSetFunctionNameID:                 {"function", "name"},
	OpLogShadowChickenTailID:            {"thisValue", "scope"},
	OpPutByValID:                        {"base", "property", "value"},
	OpPutByValDirectID:                  {"base", "property", "value"},
	OpPutByIdID:                         {"base", "value"},
	OpPutToScopeID:                      {"scope", "value"},
	OpPutToArgumentsID:                  {"arguments", "value"},
	OpPutByIdWithThisID:                 {"base", "thisValue", "value"},
	OpPutByValWithThisID:                {"base", "thisValue", "property", "value"},
	OpPutGetterByIdID:                   {"base", "accessor"},
	OpPutSetterByIdID:                   {"base", "accessor"},
	OpPutGetterSetterByIdID:             {"base", "getter", "setter"},
	OpPutGetterByValID:                  {"base", "property", "accessor"},
	OpPutSetterByValID:                  {"base", "property", "accessor"},
	OpDefineDataPropertyID:              {"base", "property", "value", "attributes"},
	OpDefineAccessorPropertyID:          {"base", "property", "getter", "setter", "attributes"},
	OpSpreadID:                          {"argument"},
	OpGetPropertyEnumeratorID:           {"base"},
	OpNewFuncExpID:                      {"scope"},
	OpNewGeneratorFuncExpID:             {"scope"},
	OpNewAsyncFuncExpID:                 {"scope"},
	OpCreateLexicalEnvironmentID:        {"scope", "symbolTable", "initialValue"},
	OpCreateGeneratorFrameEnvironmentID: {"scope", "symbolTable", "initialValue"},
	OpResolveScopeID:                    {"scope"},
	OpResolveScopeForHoistingFuncDeclInEvalID: {"scope"},
	OpGetFromScopeID:             {"scope"},
	OpToPrimitiveID:              {"src"},
	OpToPropertyKeyID:            {"src"},
	OpToPropertyKeyOrNumberID:    {"src"},
	OpGetByIdID:                  {"base"},
	OpGetLengthID:                {"base"},
	OpGetByIdDirectID:            {"base"},
	OpGetPrototypeOfID:           {"value"},
	OpInByIdID:                   {"base"},
	OpTypeofID:                   {"value"},
	OpIsEmptyID:                  {"operand"},
	OpTypeofIsUndefinedID:        {"operand"},
	OpTypeofIsObjectID:           {"operand"},
	OpTypeofIsFunctionID:         {"operand"},
	OpIsUndefinedOrNullID:        {"operand"},
	OpIsBooleanID:                {"operand"},
	OpIsNumberID:                 {"operand"},
	OpIsBigIntID:                 {"operand"},
	OpIsObjectID:                 {"operand"},
	OpIsCellWithTypeID:           {"operand"},
	OpIsCallableID:               {"operand"},
	OpIteratorCloseCheckID:       {"iterator", "next", "iterable"},
	OpIsConstructorID:            {"operand"},
	OpToNumberID:                 {"operand"},
	OpToNumericID:                {"operand"},
	OpToStringID:                 {"operand"},
	OpToObjectID:                 {"operand"},
	OpNegateID:                   {"operand"},
	OpBitnotID:                   {"operand"},
	OpEqNullID:                   {"operand"},
	OpNeqNullID:                  {"operand"},
	OpNotID:                      {"operand"},
	OpUnsignedID:                 {"operand"},
	OpMovID:                      {"src"},
	OpNewArrayWithSizeID:         {"length"},
	OpNewArrayWithSpeciesID:      {"length", "array"},
	OpCreateThisID:               {"callee"},
	OpCreatePromiseID:            {"callee"},
	OpCreateGeneratorID:          {"callee"},
	OpCreateAsyncGeneratorID:     {"callee"},
	OpDelByIdID:                  {"base"},
	OpNewFuncID:                  {"scope"},
	OpNewAsyncGeneratorFuncID:    {"scope"},
	OpNewAsyncGeneratorFuncExpID: {"scope"},
	OpNewGeneratorFuncID:         {"scope"},
	OpNewAsyncFuncID:             {"scope"},
	OpGetParentScopeID:           {"scope"},
	OpCreateScopedArgumentsID:    {"scope"},
	OpGetFromArgumentsID:         {"arguments"},
	OpNewArrayBufferID:           {"immutableButterfly"},
	OpGetByValID:                 {"base", "property"},
	OpGetPrivateNameID:           {"base", "property"},
	OpPutPrivateNameID:           {"base", "property", "value"},
	OpSetPrivateBrandID:          {"base", "brand"},
	OpCheckPrivateBrandID:        {"base", "brand"},
	OpInByValID:                  {"base", "property"},
	OpHasPrivateNameID:           {"base", "property"},
	OpHasPrivateBrandID:          {"base", "brand"},
	OpHasStructureWithFlagsID:    {"operand"},
	OpAddID:                      {"lhs", "rhs"},
	OpMulID:                      {"lhs", "rhs"},
	OpDivID:                      {"lhs", "rhs"},
	OpModID:                      {"lhs", "rhs"},
	OpSubID:                      {"lhs", "rhs"},
	OpPowID:                      {"lhs", "rhs"},
	OpLshiftID:                   {"lhs", "rhs"},
	OpRshiftID:                   {"lhs", "rhs"},
	OpUrshiftID:                  {"lhs", "rhs"},
	OpBitandID:                   {"lhs", "rhs"},
	OpBitxorID:                   {"lhs", "rhs"},
	OpBitorID:                    {"lhs", "rhs"},
	OpLessID:                     {"lhs", "rhs"},
	OpLesseqID:                   {"lhs", "rhs"},
	OpGreaterID:                  {"lhs", "rhs"},
	OpGreatereqID:                {"lhs", "rhs"},
	OpBelowID:                    {"lhs", "rhs"},
	OpBeloweqID:                  {"lhs", "rhs"},
	OpNstricteqID:                {"lhs", "rhs"},
	OpStricteqID:                 {"lhs", "rhs"},
	OpNeqID:                      {"lhs", "rhs"},
	OpEqID:                       {"lhs", "rhs"},
	OpPushWithScopeID:            {"currentScope", "newScope"},
	OpGetByIdWithThisID:          {"base", "thisValue"},
	OpDelByValID:                 {"base", "property"},
	OpGetByValWithThisID:         {"base", "thisValue", "property"},
	OpCallVarargsID:              {"callee", "thisValue", "arguments"},
	OpTailCallVarargsID:          {"callee", "thisValue", "arguments"},
	OpConstructVarargsID:         {"callee", "thisValue", "arguments"},
	OpSuperConstructVarargsID:    {"callee", "thisValue", "arguments"},
	OpSwitchStringID:             {"scrutinee"},
	OpSwitchCharID:               {"scrutinee"},
	OpSwitchImmID:                {"scrutinee"},
	OpGetInternalFieldID:         {"base"},
	OpPutInternalFieldID:         {"base", "value"},
	OpYieldID:                    {"argument"},
	OpEnumeratorNextID:           {"mode", "index", "base", "enumerator"},
	OpEnumeratorGetByValID:       {"base", "mode", "propertyName", "index", "enumerator"},
	OpEnumeratorInByValID:        {"base", "mode", "propertyName", "index", "enumerator"},
	OpEnumeratorPutByValID:       {"base", "mode", "propertyName", "index", "enumerator", "value"},
	OpEnumeratorHasOwnPropertyID: {"base", "mode", "propertyName", "index", "enumerator"},
})

var simpleDefs = buildOperandTable(map[OpcodeID][]string{
	// These don't define anything.
	OpPutToScopeID: {}, OpThrowID: {}, OpThrowStaticErrorID: {}, OpCheckTdzID: {}, OpDebugID: {},
	OpRetID: {}, OpJmpID: {}, OpJtrueID: {}, OpJfalseID: {}, OpJeqNullID: {}, OpJneqNullID: {},
	OpJundefinedOrNullID: {}, OpJnundefinedOrNullID: {}, OpJeqPtrID: {}, OpJneqPtrID: {},
	OpJlessID: {}, OpJlesseqID: {}, OpJgreaterID: {}, OpJgreatereqID: {}, OpJnlessID: {},
	OpJnlesseqID: {}, OpJngreaterID: {}, OpJngreatereqID: {}, OpJeqID: {}, OpJneqID: {},
	OpJstricteqID: {}, OpJnstricteqID: {}, OpJbelowID: {}, OpJbeloweqID: {}, OpLoopHintID: {},
	OpSwitchImmID: {}, OpSwitchCharID: {}, OpSwitchStringID: {}, OpPutByIdID: {},
	OpPutByIdWithThisID: {}, OpPutByValWithThisID: {}, OpPutGetterByIdID: {}, OpPutSetterByIdID: {},
	OpPutGetterSetterByIdID: {}, OpPutGetterByValID: {}, OpPutSetterByValID: {}, OpPutByValID: {},
	OpPutByValDirectID: {}, OpEnumeratorPutByValID: {}, OpPutPrivateNameID: {}, OpSetPrivateBrandID: {},
	OpCheckPrivateBrandID: {}, OpPutInternalFieldID: {}, OpDefineDataPropertyID: {},
	OpDefineAccessorPropertyID: {}, OpProfileTypeID: {}, OpProfileControlFlowID: {},
	OpPutToArgumentsID: {}, OpCallIgnoreResultID: {}, OpSetFunctionNameID: {}, OpCheckTrapsID: {},
	OpLogShadowChickenPrologueID: {}, OpLogShadowChickenTailID: {}, OpYieldID: {}, OpNopID: {},
	OpUnreachableID: {}, OpSuperSamplerBeginID: {}, OpSuperSamplerEndID: {},

	// A single destination.
	OpArgumentCountID:                         {"dst"},
	OpGetPropertyEnumeratorID:                 {"dst"},
	OpGetParentScopeID:                        {"dst"},
	OpPushWithScopeID:                         {"dst"},
	OpCreateLexicalEnvironmentID:              {"dst"},
	OpCreateGeneratorFrameEnvironmentID:       {"dst"},
	OpResolveScopeID:                          {"dst"},
	OpResolveScopeForHoistingFuncDeclInEvalID: {"dst"},
	OpStrcatID:                                {"dst"},
	OpToPrimitiveID:                           {"dst"},
	OpToPropertyKeyID:                         {"dst"},
	OpToPropertyKeyOrNumberID:                 {"dst"},
	OpCreateThisID:                            {"dst"},
	OpCreatePromiseID:                         {"dst"},
	OpCreateGeneratorID:                       {"dst"},
	OpCreateAsyncGeneratorID:                  {"dst"},
	OpNewArrayID:                              {"dst"},
	OpNewArrayWithSpreadID:                    {"dst"},
	OpSpreadID:                                {"dst"},
	OpNewArrayBufferID:                        {"dst"},
	OpNewArrayWithSizeID:                      {"dst"},
	OpNewArrayWithSpeciesID:                   {"dst"},
	OpNewRegExpID:                             {"dst"},
	OpNewFuncID:                               {"dst"},
	OpNewFuncExpID:                            {"dst"},
	OpNewGeneratorFuncID:                      {"dst"},
	OpNewGeneratorFuncExpID:                   {"dst"},
	OpNewAsyncGeneratorFuncID:                 {"dst"},
	OpNewAsyncGeneratorFuncExpID:              {"dst"},
	OpNewAsyncFuncID:                          {"dst"},
	OpNewAsyncFuncExpID:                       {"dst"},
	OpGetFromScopeID:                          {"dst"},
	OpCallID:                                  {"dst"},
	OpTailCallID:                              {"dst"},
	OpCallDirectEvalID:                        {"dst"},
	OpConstructID:                             {"dst"},
	OpSuperConstructID:                        {"dst"},
	OpGetByIdID:                               {"dst"},
	OpAsyncIteratorNextID:                     {"dst"},
	OpGetLengthID:                             {"dst"},
	OpGetByIdDirectID:                         {"dst"},
	OpGetByIdWithThisID:                       {"dst"},
	OpGetByValWithThisID:                      {"dst"},
	OpGetPrototypeOfID:                        {"dst"},
	OpGetByValID:                              {"dst"},
	OpGetPrivateNameID:                        {"dst"},
	OpTypeofID:                                {"dst"},
	OpIdentityWithProfileID:                   {"srcDst"},
	OpIsEmptyID:                               {"dst"},
	OpTypeofIsUndefinedID:                     {"dst"},
	OpTypeofIsObjectID:                        {"dst"},
	OpTypeofIsFunctionID:                      {"dst"},
	OpIsUndefinedOrNullID:                     {"dst"},
	OpIsBooleanID:                             {"dst"},
	OpIsNumberID:                              {"dst"},
	OpIsBigIntID:                              {"dst"},
	OpIsObjectID:                              {"dst"},
	OpIsCellWithTypeID:                        {"dst"},
	OpIsCallableID:                            {"dst"},
	OpIteratorCloseCheckID:                    {"iterator"},
	OpIsConstructorID:                         {"dst"},
	OpInByIdID:                                {"dst"},
	OpInByValID:                               {"dst"},
	OpHasPrivateNameID:                        {"dst"},
	OpHasPrivateBrandID:                       {"dst"},
	OpHasStructureWithFlagsID:                 {"dst"},
	OpToNumberID:                              {"dst"},
	OpToNumericID:                             {"dst"},
	OpToStringID:                              {"dst"},
	OpToObjectID:                              {"dst"},
	OpNegateID:                                {"dst"},
	OpAddID:                                   {"dst"},
	OpMulID:                                   {"dst"},
	OpDivID:                                   {"dst"},
	OpModID:                                   {"dst"},
	OpSubID:                                   {"dst"},
	OpPowID:                                   {"dst"},
	OpLshiftID:                                {"dst"},
	OpRshiftID:                                {"dst"},
	OpUrshiftID:                               {"dst"},
	OpBitandID:                                {"dst"},
	OpBitxorID:                                {"dst"},
	OpBitorID:                                 {"dst"},
	OpBitnotID:                                {"dst"},
	OpIncID:                                   {"srcDst"},
	OpDecID:                                   {"srcDst"},
	OpEqID:                                    {"dst"},
	OpNeqID:                                   {"dst"},
	OpStricteqID:                              {"dst"},
	OpNstricteqID:                             {"dst"},
	OpLessID:                                  {"dst"},
	OpLesseqID:                                {"dst"},
	OpGreaterID:                               {"dst"},
	OpGreatereqID:                             {"dst"},
	OpBelowID:                                 {"dst"},
	OpBeloweqID:                               {"dst"},
	OpNeqNullID:                               {"dst"},
	OpEqNullID:                                {"dst"},
	OpNotID:                                   {"dst"},
	OpMovID:                                   {"dst"},
	OpNewObjectID:                             {"dst"},
	OpNewPromiseID:                            {"dst"},
	OpNewGeneratorID:                          {"dst"},
	OpNewAsyncFunctionGeneratorID:             {"dst"},
	OpToThisID:                                {"srcDst"},
	OpGetScopeID:                              {"dst"},
	OpCreateDirectArgumentsID:                 {"dst"},
	OpCreateScopedArgumentsID:                 {"dst"},
	OpCreateClonedArgumentsID:                 {"dst"},
	OpDelByIdID:                               {"dst"},
	OpDelByValID:                              {"dst"},
	OpUnsignedID:                              {"dst"},
	OpGetFromArgumentsID:                      {"dst"},
	OpGetArgumentID:                           {"dst"},
	OpCreateRestID:                            {"dst"},
	OpGetInternalFieldID:                      {"dst"},
	OpCatchID:                                 {"exception", "thrownValue"},
	OpEnumeratorNextID:                        {"propertyName", "mode", "index"},
	OpEnumeratorGetByValID:                    {"dst"},
	OpEnumeratorInByValID:                     {"dst"},
	OpEnumeratorHasOwnPropertyID:              {"dst"},
})

func operand(ins []uint32, index int) VirtualRegister { return VirtualRegister(int32(ins[index])) }

// ComputeUsesForInstruction calls f for each register the instruction ins
// reads at checkpoint.
func ComputeUsesForInstruction(cb *UnlinkedCodeBlock, ins []uint32, checkpoint int, f func(VirtualRegister)) {
	op := OpcodeID(ins[0])
	if op != OpEnterID && cb.WasCompiledWithDebuggingOpcodes() && cb.ScopeRegister.IsValid() {
		f(cb.ScopeRegister)
	}
	computeUsesImpl(ins, checkpoint, f)
}

func computeUsesImpl(ins []uint32, checkpoint int, f func(VirtualRegister)) {
	op := OpcodeID(ins[0])
	if indices := simpleUses[op]; indices != nil {
		for _, i := range indices {
			f(operand(ins, i))
		}
		return
	}

	// handleOpCallLike: the callee and the arguments, which start at
	// "this" in the outgoing frame.
	callLike := func(callee VirtualRegister, argc, argv uint32) {
		f(callee)
		lastArg := -int(argv) + CallFrameSlotThisArgument
		for i := 0; i < int(argc); i++ {
			f(VirtualRegister(lastArg + i))
		}
	}
	// handleNewArrayLike: argc registers counting down from argv.
	newArrayLike := func(argv VirtualRegister, argc uint32) {
		for i := 0; i < int(argc); i++ {
			f(argv - VirtualRegister(i))
		}
	}

	switch op {
	case OpIteratorOpenID:
		b := DecodeIteratorOpen(ins)
		if checkpoint >= OpIteratorOpenSymbolCallCheckpoint {
			f(b.SymbolIterator)
			f(b.Iterable)
		}
		if checkpoint >= OpIteratorOpenGetNextCheckpoint {
			f(b.Iterator)
		}
	case OpAsyncIteratorOpenID:
		b := DecodeAsyncIteratorOpen(ins)
		if checkpoint >= OpAsyncIteratorOpenSymbolCallCheckpoint {
			f(b.SymbolIterator)
			f(b.Iterable)
		}
		if checkpoint >= OpAsyncIteratorOpenGetNextCheckpoint {
			f(b.Iterator)
		}
	case OpIteratorNextID:
		b := DecodeIteratorNext(ins)
		f(b.Iterator)
		f(b.Next)
		if checkpoint >= OpIteratorNextComputeNextCheckpoint {
			f(b.Iterable)
		}
	case OpAsyncIteratorNextID:
		b := DecodeAsyncIteratorNext(ins)
		f(b.Next)
		f(b.Iterator)
		f(b.Driver)
		// The resume value is call argument 1 of the outgoing frame.
		if b.HasValue {
			f(AsyncIteratorNextResumeValueOperand(b))
		}
	case OpNewArrayWithSpreadID:
		b := DecodeNewArrayWithSpread(ins)
		newArrayLike(b.Argv, b.Argc)
	case OpNewArrayID:
		b := DecodeNewArray(ins)
		newArrayLike(b.Argv, b.Argc)
	case OpStrcatID:
		b := DecodeStrcat(ins)
		for i := int32(0); i < b.Count; i++ {
			f(b.Src - VirtualRegister(i))
		}
	case OpConstructID:
		b := DecodeConstruct(ins)
		callLike(b.Callee, b.Argc, b.Argv)
	case OpSuperConstructID:
		b := DecodeSuperConstruct(ins)
		callLike(b.Callee, b.Argc, b.Argv)
	case OpCallDirectEvalID:
		b := DecodeCallDirectEval(ins)
		callLike(b.Callee, b.Argc, b.Argv)
		f(b.ThisValue)
		f(b.Scope)
	case OpCallID:
		b := DecodeCall(ins)
		callLike(b.Callee, b.Argc, b.Argv)
	case OpTailCallID:
		b := DecodeTailCall(ins)
		callLike(b.Callee, b.Argc, b.Argv)
	case OpCallIgnoreResultID:
		b := DecodeCallIgnoreResult(ins)
		callLike(b.Callee, b.Argc, b.Argv)
	case OpInstanceofID:
		b := DecodeInstanceof(ins)
		switch checkpoint {
		case OpInstanceofGetHasInstanceCheckpoint:
			f(b.Constructor)
		case OpInstanceofGetPrototypeCheckpoint:
			f(b.Value)
			f(b.Constructor)
			f(b.HasInstanceOrPrototype)
		case OpInstanceofInstanceofCheckpoint:
			f(b.Value)
			f(b.HasInstanceOrPrototype)
		}
	default:
		panic("bytecode: no use information for " + op.String())
	}
}

// AsyncIteratorNextResumeValueOperand is the register of the value passed
// to next() (resumeValueOperandFor).
func AsyncIteratorNextResumeValueOperand(b OpAsyncIteratorNext) VirtualRegister {
	return VirtualRegisterForArgumentIncludingThis(1, -int(b.StackOffset))
}

// ComputeDefsForInstruction calls f for each register the instruction ins
// writes at checkpoint.
func ComputeDefsForInstruction(cb *UnlinkedCodeBlock, ins []uint32, checkpoint int, f func(VirtualRegister)) {
	computeDefsImpl(cb.NumVars, ins, checkpoint, f)
}

func computeDefsImpl(numVars int, ins []uint32, checkpoint int, f func(VirtualRegister)) {
	op := OpcodeID(ins[0])
	if indices := simpleDefs[op]; indices != nil {
		for _, i := range indices {
			f(operand(ins, i))
		}
		return
	}

	defAt := func(target int, r VirtualRegister) {
		if target == checkpoint {
			f(r)
		}
	}

	switch op {
	case OpCallVarargsID:
		defAt(OpCallVarargsMakeCallCheckpoint, DecodeCallVarargs(ins).Dst)
	case OpTailCallVarargsID:
		defAt(OpTailCallVarargsMakeCallCheckpoint, DecodeTailCallVarargs(ins).Dst)
	case OpConstructVarargsID:
		defAt(OpConstructVarargsMakeCallCheckpoint, DecodeConstructVarargs(ins).Dst)
	case OpSuperConstructVarargsID:
		defAt(OpSuperConstructVarargsMakeCallCheckpoint, DecodeSuperConstructVarargs(ins).Dst)
	case OpIteratorOpenID:
		b := DecodeIteratorOpen(ins)
		defAt(OpIteratorOpenSymbolCallCheckpoint, b.Iterator)
		defAt(OpIteratorOpenGetNextCheckpoint, b.Next)
	case OpAsyncIteratorOpenID:
		b := DecodeAsyncIteratorOpen(ins)
		defAt(OpAsyncIteratorOpenSymbolCallCheckpoint, b.Iterator)
		defAt(OpAsyncIteratorOpenGetNextCheckpoint, b.Next)
	case OpIteratorNextID:
		b := DecodeIteratorNext(ins)
		defAt(OpIteratorNextComputeNextCheckpoint, b.Next)
		defAt(OpIteratorNextGetDoneCheckpoint, b.Done)
		// value is set at getDone too: we may exit early when done.
		defAt(OpIteratorNextGetDoneCheckpoint, b.Value)
		defAt(OpIteratorNextGetValueCheckpoint, b.Value)
	case OpEnterID:
		for i := numVars - 1; i >= 0; i-- {
			f(VirtualRegisterForLocal(i))
		}
	case OpInstanceofID:
		b := DecodeInstanceof(ins)
		defAt(OpInstanceofGetHasInstanceCheckpoint, b.HasInstanceOrPrototype)
		defAt(OpInstanceofGetPrototypeCheckpoint, b.HasInstanceOrPrototype)
		defAt(OpInstanceofInstanceofCheckpoint, b.Dst)
	}
}
