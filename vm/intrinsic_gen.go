// Code generated from JavaScriptCore/runtime/Intrinsic.h; DO NOT EDIT.

package vm

// Intrinsic is JSC's Intrinsic: what a builtin function is, which lets the
// JIT run it itself (the thunks in jit_thunks.go).
type Intrinsic uint16

const (
	NoIntrinsic Intrinsic = iota
	AbsIntrinsic
	ACosIntrinsic
	ASinIntrinsic
	ATanIntrinsic
	ACoshIntrinsic
	ASinhIntrinsic
	ATanhIntrinsic
	MinIntrinsic
	MaxIntrinsic
	SqrtIntrinsic
	SinIntrinsic
	CbrtIntrinsic
	Clz32Intrinsic
	CosIntrinsic
	TanIntrinsic
	CoshIntrinsic
	SinhIntrinsic
	TanhIntrinsic
	ArrayPushIntrinsic
	ArrayPopIntrinsic
	ArrayShiftIntrinsic
	ArrayUnshiftIntrinsic
	ArrayConcatIntrinsic
	ArraySliceIntrinsic
	ArraySpliceIntrinsic
	ArrayIncludesIntrinsic
	ArrayIndexOfIntrinsic
	ArrayJoinIntrinsic
	ArraySortIntrinsic
	ArrayValuesIntrinsic
	ArrayKeysIntrinsic
	ArrayEntriesIntrinsic
	ArrayConstructorOfIntrinsic
	ArrayIsArrayIntrinsic
	AsyncIteratorIntrinsic
	BooleanConstructorIntrinsic
	CharCodeAtIntrinsic
	CharAtIntrinsic
	DateNowIntrinsic
	DatePrototypeGetTimeIntrinsic
	DatePrototypeGetFullYearIntrinsic
	DatePrototypeGetUTCFullYearIntrinsic
	DatePrototypeGetMonthIntrinsic
	DatePrototypeGetUTCMonthIntrinsic
	DatePrototypeGetDateIntrinsic
	DatePrototypeGetUTCDateIntrinsic
	DatePrototypeGetDayIntrinsic
	DatePrototypeGetUTCDayIntrinsic
	DatePrototypeGetHoursIntrinsic
	DatePrototypeGetUTCHoursIntrinsic
	DatePrototypeGetMinutesIntrinsic
	DatePrototypeGetUTCMinutesIntrinsic
	DatePrototypeGetSecondsIntrinsic
	DatePrototypeGetUTCSecondsIntrinsic
	DatePrototypeGetMillisecondsIntrinsic
	DatePrototypeGetUTCMillisecondsIntrinsic
	DatePrototypeGetTimezoneOffsetIntrinsic
	DatePrototypeGetYearIntrinsic
	DatePrototypeSetTimeIntrinsic
	ErrorIsErrorIntrinsic
	FromCharCodeIntrinsic
	FromCodePointIntrinsic
	GlobalIsFiniteIntrinsic
	GlobalIsNaNIntrinsic
	PowIntrinsic
	FloorIntrinsic
	CeilIntrinsic
	RoundIntrinsic
	ExpIntrinsic
	Expm1Intrinsic
	LogIntrinsic
	Log10Intrinsic
	Log1pIntrinsic
	Log2Intrinsic
	RegExpExecIntrinsic
	RegExpTestIntrinsic
	RegExpMatchIntrinsic
	RegExpSearchIntrinsic
	RegExpSplitIntrinsic
	ObjectAssignIntrinsic
	ObjectCreateIntrinsic
	ObjectDefinePropertyIntrinsic
	ObjectGetOwnPropertyNamesIntrinsic
	ObjectGetOwnPropertySymbolsIntrinsic
	ObjectGetPrototypeOfIntrinsic
	ObjectHasOwnIntrinsic
	ObjectIsIntrinsic
	ObjectKeysIntrinsic
	ObjectToStringIntrinsic
	ReflectGetPrototypeOfIntrinsic
	ReflectOwnKeysIntrinsic
	StringConstructorIntrinsic
	StringPrototypeConcatIntrinsic
	StringPrototypeAtIntrinsic
	StringPrototypeCodePointAtIntrinsic
	StringPrototypeIndexOfIntrinsic
	StringPrototypeLastIndexOfIntrinsic
	StringPrototypeIncludesIntrinsic
	StringPrototypeStartsWithIntrinsic
	StringPrototypeEndsWithIntrinsic
	StringPrototypeLocaleCompareIntrinsic
	StringPrototypeValueOfIntrinsic
	StringPrototypeMatchIntrinsic
	StringPrototypeSearchIntrinsic
	StringPrototypeReplaceIntrinsic
	StringPrototypeReplaceAllIntrinsic
	StringPrototypeSplitIntrinsic
	StringPrototypeSliceIntrinsic
	StringPrototypeSubstringIntrinsic
	StringPrototypeSubstrIntrinsic
	StringPrototypeToLowerCaseIntrinsic
	StringPrototypeToUpperCaseIntrinsic
	StringPrototypeTrimIntrinsic
	StringPrototypeTrimStartIntrinsic
	StringPrototypeTrimEndIntrinsic
	SymbolPrototypeToStringIntrinsic
	NumberPrototypeToStringIntrinsic
	NumberIsFiniteIntrinsic
	NumberIsNaNIntrinsic
	NumberIsSafeIntegerIntrinsic
	NumberIsIntegerIntrinsic
	NumberConstructorIntrinsic
	IMulIntrinsic
	RandomIntrinsic
	FRoundIntrinsic
	F16RoundIntrinsic
	ToIntegerOrInfinityIntrinsic
	ToLengthIntrinsic
	TruncIntrinsic
	TypedArrayValuesIntrinsic
	TypedArrayKeysIntrinsic
	TypedArrayEntriesIntrinsic
	IsTypedArrayViewIntrinsic
	ArrayBufferIsViewIntrinsic
	BoundFunctionCallIntrinsic
	RemoteFunctionCallIntrinsic
	IteratorIntrinsic
	JSMapGetIntrinsic
	JSMapHasIntrinsic
	JSMapSetIntrinsic
	JSMapDeleteIntrinsic
	JSMapValuesIntrinsic
	JSMapKeysIntrinsic
	JSMapEntriesIntrinsic
	JSMapStorageIntrinsic
	JSMapIterationNextIntrinsic
	JSMapIterationEntryIntrinsic
	JSMapIterationEntryKeyIntrinsic
	JSMapIterationEntryValueIntrinsic
	JSSetStorageIntrinsic
	JSSetIterationNextIntrinsic
	JSSetIterationEntryIntrinsic
	JSSetIterationEntryKeyIntrinsic
	JSMapIteratorNextIntrinsic
	JSSetIteratorNextIntrinsic
	JSSetHasIntrinsic
	JSSetAddIntrinsic
	JSSetDeleteIntrinsic
	JSSetValuesIntrinsic
	JSSetEntriesIntrinsic
	JSStringIteratorIntrinsic
	JSStringIteratorNextIntrinsic
	JSWeakMapGetIntrinsic
	JSWeakMapHasIntrinsic
	JSWeakMapSetIntrinsic
	JSWeakSetHasIntrinsic
	JSWeakSetAddIntrinsic
	HasOwnPropertyIntrinsic
	AtomicsAddIntrinsic
	AtomicsAndIntrinsic
	AtomicsCompareExchangeIntrinsic
	AtomicsExchangeIntrinsic
	AtomicsIsLockFreeIntrinsic
	AtomicsLoadIntrinsic
	AtomicsNotifyIntrinsic
	AtomicsOrIntrinsic
	AtomicsPauseIntrinsic
	AtomicsStoreIntrinsic
	AtomicsSubIntrinsic
	AtomicsWaitIntrinsic
	AtomicsWaitAsyncIntrinsic
	AtomicsXorIntrinsic
	ParseIntIntrinsic
	FunctionToStringIntrinsic
	FunctionBindIntrinsic
	IteratorHelperCreateIntrinsic
	WrapForValidIteratorCreateIntrinsic
	RegExpStringIteratorCreateIntrinsic
	RegExpStringIteratorNextIntrinsic
	ResolvePromiseWithFirstResolvingFunctionCallCheckIntrinsic
	RejectPromiseWithFirstResolvingFunctionCallCheckIntrinsic
	FulfillPromiseWithFirstResolvingFunctionCallCheckIntrinsic
	NewResolvedPromiseIntrinsic
	NewRejectedPromiseIntrinsic
	PromiseConstructorResolveIntrinsic
	PromiseResolveIntrinsic
	PromiseConstructorRejectIntrinsic
	PromiseRejectIntrinsic
	PromisePrototypeThenIntrinsic
	PromisePrototypeCatchIntrinsic
	TypedArrayLengthIntrinsic
	TypedArrayByteLengthIntrinsic
	DataViewByteLengthIntrinsic
	TypedArrayByteOffsetIntrinsic
	UnderscoreProtoIntrinsic
	SpeciesGetterIntrinsic
	WebAssemblyInstanceExportsIntrinsic
	JSSetSizeIntrinsic
	JSMapSizeIntrinsic
	RegExpHasIndicesIntrinsic
	RegExpGlobalIntrinsic
	RegExpIgnoreCaseIntrinsic
	RegExpMultilineIntrinsic
	RegExpDotAllIntrinsic
	RegExpUnicodeIntrinsic
	RegExpUnicodeSetsIntrinsic
	RegExpStickyIntrinsic
	DFGTrueIntrinsic
	FTLTrueIntrinsic
	OSRExitIntrinsic
	IsFinalTierIntrinsic
	SetInt32HeapPredictionIntrinsic
	CheckInt32Intrinsic
	FiatInt52Intrinsic
	CPUMfenceIntrinsic
	CPURdtscIntrinsic
	CPUCpuidIntrinsic
	CPUPauseIntrinsic
	DataViewGetInt8
	DataViewGetUint8
	DataViewGetInt16
	DataViewGetUint16
	DataViewGetInt32
	DataViewGetUint32
	DataViewGetFloat16
	DataViewGetFloat32
	DataViewGetFloat64
	DataViewGetBigInt64
	DataViewGetBigUint64
	DataViewSetInt8
	DataViewSetUint8
	DataViewSetInt16
	DataViewSetUint16
	DataViewSetInt32
	DataViewSetUint32
	DataViewSetFloat16
	DataViewSetFloat32
	DataViewSetFloat64
	DataViewSetBigInt64
	DataViewSetBigUint64
	WasmFunctionIntrinsic
)

var intrinsicNames = [...]string{
	NoIntrinsic:                              "NoIntrinsic",
	AbsIntrinsic:                             "AbsIntrinsic",
	ACosIntrinsic:                            "ACosIntrinsic",
	ASinIntrinsic:                            "ASinIntrinsic",
	ATanIntrinsic:                            "ATanIntrinsic",
	ACoshIntrinsic:                           "ACoshIntrinsic",
	ASinhIntrinsic:                           "ASinhIntrinsic",
	ATanhIntrinsic:                           "ATanhIntrinsic",
	MinIntrinsic:                             "MinIntrinsic",
	MaxIntrinsic:                             "MaxIntrinsic",
	SqrtIntrinsic:                            "SqrtIntrinsic",
	SinIntrinsic:                             "SinIntrinsic",
	CbrtIntrinsic:                            "CbrtIntrinsic",
	Clz32Intrinsic:                           "Clz32Intrinsic",
	CosIntrinsic:                             "CosIntrinsic",
	TanIntrinsic:                             "TanIntrinsic",
	CoshIntrinsic:                            "CoshIntrinsic",
	SinhIntrinsic:                            "SinhIntrinsic",
	TanhIntrinsic:                            "TanhIntrinsic",
	ArrayPushIntrinsic:                       "ArrayPushIntrinsic",
	ArrayPopIntrinsic:                        "ArrayPopIntrinsic",
	ArrayShiftIntrinsic:                      "ArrayShiftIntrinsic",
	ArrayUnshiftIntrinsic:                    "ArrayUnshiftIntrinsic",
	ArrayConcatIntrinsic:                     "ArrayConcatIntrinsic",
	ArraySliceIntrinsic:                      "ArraySliceIntrinsic",
	ArraySpliceIntrinsic:                     "ArraySpliceIntrinsic",
	ArrayIncludesIntrinsic:                   "ArrayIncludesIntrinsic",
	ArrayIndexOfIntrinsic:                    "ArrayIndexOfIntrinsic",
	ArrayJoinIntrinsic:                       "ArrayJoinIntrinsic",
	ArraySortIntrinsic:                       "ArraySortIntrinsic",
	ArrayValuesIntrinsic:                     "ArrayValuesIntrinsic",
	ArrayKeysIntrinsic:                       "ArrayKeysIntrinsic",
	ArrayEntriesIntrinsic:                    "ArrayEntriesIntrinsic",
	ArrayConstructorOfIntrinsic:              "ArrayConstructorOfIntrinsic",
	ArrayIsArrayIntrinsic:                    "ArrayIsArrayIntrinsic",
	AsyncIteratorIntrinsic:                   "AsyncIteratorIntrinsic",
	BooleanConstructorIntrinsic:              "BooleanConstructorIntrinsic",
	CharCodeAtIntrinsic:                      "CharCodeAtIntrinsic",
	CharAtIntrinsic:                          "CharAtIntrinsic",
	DateNowIntrinsic:                         "DateNowIntrinsic",
	DatePrototypeGetTimeIntrinsic:            "DatePrototypeGetTimeIntrinsic",
	DatePrototypeGetFullYearIntrinsic:        "DatePrototypeGetFullYearIntrinsic",
	DatePrototypeGetUTCFullYearIntrinsic:     "DatePrototypeGetUTCFullYearIntrinsic",
	DatePrototypeGetMonthIntrinsic:           "DatePrototypeGetMonthIntrinsic",
	DatePrototypeGetUTCMonthIntrinsic:        "DatePrototypeGetUTCMonthIntrinsic",
	DatePrototypeGetDateIntrinsic:            "DatePrototypeGetDateIntrinsic",
	DatePrototypeGetUTCDateIntrinsic:         "DatePrototypeGetUTCDateIntrinsic",
	DatePrototypeGetDayIntrinsic:             "DatePrototypeGetDayIntrinsic",
	DatePrototypeGetUTCDayIntrinsic:          "DatePrototypeGetUTCDayIntrinsic",
	DatePrototypeGetHoursIntrinsic:           "DatePrototypeGetHoursIntrinsic",
	DatePrototypeGetUTCHoursIntrinsic:        "DatePrototypeGetUTCHoursIntrinsic",
	DatePrototypeGetMinutesIntrinsic:         "DatePrototypeGetMinutesIntrinsic",
	DatePrototypeGetUTCMinutesIntrinsic:      "DatePrototypeGetUTCMinutesIntrinsic",
	DatePrototypeGetSecondsIntrinsic:         "DatePrototypeGetSecondsIntrinsic",
	DatePrototypeGetUTCSecondsIntrinsic:      "DatePrototypeGetUTCSecondsIntrinsic",
	DatePrototypeGetMillisecondsIntrinsic:    "DatePrototypeGetMillisecondsIntrinsic",
	DatePrototypeGetUTCMillisecondsIntrinsic: "DatePrototypeGetUTCMillisecondsIntrinsic",
	DatePrototypeGetTimezoneOffsetIntrinsic:  "DatePrototypeGetTimezoneOffsetIntrinsic",
	DatePrototypeGetYearIntrinsic:            "DatePrototypeGetYearIntrinsic",
	DatePrototypeSetTimeIntrinsic:            "DatePrototypeSetTimeIntrinsic",
	ErrorIsErrorIntrinsic:                    "ErrorIsErrorIntrinsic",
	FromCharCodeIntrinsic:                    "FromCharCodeIntrinsic",
	FromCodePointIntrinsic:                   "FromCodePointIntrinsic",
	GlobalIsFiniteIntrinsic:                  "GlobalIsFiniteIntrinsic",
	GlobalIsNaNIntrinsic:                     "GlobalIsNaNIntrinsic",
	PowIntrinsic:                             "PowIntrinsic",
	FloorIntrinsic:                           "FloorIntrinsic",
	CeilIntrinsic:                            "CeilIntrinsic",
	RoundIntrinsic:                           "RoundIntrinsic",
	ExpIntrinsic:                             "ExpIntrinsic",
	Expm1Intrinsic:                           "Expm1Intrinsic",
	LogIntrinsic:                             "LogIntrinsic",
	Log10Intrinsic:                           "Log10Intrinsic",
	Log1pIntrinsic:                           "Log1pIntrinsic",
	Log2Intrinsic:                            "Log2Intrinsic",
	RegExpExecIntrinsic:                      "RegExpExecIntrinsic",
	RegExpTestIntrinsic:                      "RegExpTestIntrinsic",
	RegExpMatchIntrinsic:                     "RegExpMatchIntrinsic",
	RegExpSearchIntrinsic:                    "RegExpSearchIntrinsic",
	RegExpSplitIntrinsic:                     "RegExpSplitIntrinsic",
	ObjectAssignIntrinsic:                    "ObjectAssignIntrinsic",
	ObjectCreateIntrinsic:                    "ObjectCreateIntrinsic",
	ObjectDefinePropertyIntrinsic:            "ObjectDefinePropertyIntrinsic",
	ObjectGetOwnPropertyNamesIntrinsic:       "ObjectGetOwnPropertyNamesIntrinsic",
	ObjectGetOwnPropertySymbolsIntrinsic:     "ObjectGetOwnPropertySymbolsIntrinsic",
	ObjectGetPrototypeOfIntrinsic:            "ObjectGetPrototypeOfIntrinsic",
	ObjectHasOwnIntrinsic:                    "ObjectHasOwnIntrinsic",
	ObjectIsIntrinsic:                        "ObjectIsIntrinsic",
	ObjectKeysIntrinsic:                      "ObjectKeysIntrinsic",
	ObjectToStringIntrinsic:                  "ObjectToStringIntrinsic",
	ReflectGetPrototypeOfIntrinsic:           "ReflectGetPrototypeOfIntrinsic",
	ReflectOwnKeysIntrinsic:                  "ReflectOwnKeysIntrinsic",
	StringConstructorIntrinsic:               "StringConstructorIntrinsic",
	StringPrototypeConcatIntrinsic:           "StringPrototypeConcatIntrinsic",
	StringPrototypeAtIntrinsic:               "StringPrototypeAtIntrinsic",
	StringPrototypeCodePointAtIntrinsic:      "StringPrototypeCodePointAtIntrinsic",
	StringPrototypeIndexOfIntrinsic:          "StringPrototypeIndexOfIntrinsic",
	StringPrototypeLastIndexOfIntrinsic:      "StringPrototypeLastIndexOfIntrinsic",
	StringPrototypeIncludesIntrinsic:         "StringPrototypeIncludesIntrinsic",
	StringPrototypeStartsWithIntrinsic:       "StringPrototypeStartsWithIntrinsic",
	StringPrototypeEndsWithIntrinsic:         "StringPrototypeEndsWithIntrinsic",
	StringPrototypeLocaleCompareIntrinsic:    "StringPrototypeLocaleCompareIntrinsic",
	StringPrototypeValueOfIntrinsic:          "StringPrototypeValueOfIntrinsic",
	StringPrototypeMatchIntrinsic:            "StringPrototypeMatchIntrinsic",
	StringPrototypeSearchIntrinsic:           "StringPrototypeSearchIntrinsic",
	StringPrototypeReplaceIntrinsic:          "StringPrototypeReplaceIntrinsic",
	StringPrototypeReplaceAllIntrinsic:       "StringPrototypeReplaceAllIntrinsic",
	StringPrototypeSplitIntrinsic:            "StringPrototypeSplitIntrinsic",
	StringPrototypeSliceIntrinsic:            "StringPrototypeSliceIntrinsic",
	StringPrototypeSubstringIntrinsic:        "StringPrototypeSubstringIntrinsic",
	StringPrototypeSubstrIntrinsic:           "StringPrototypeSubstrIntrinsic",
	StringPrototypeToLowerCaseIntrinsic:      "StringPrototypeToLowerCaseIntrinsic",
	StringPrototypeToUpperCaseIntrinsic:      "StringPrototypeToUpperCaseIntrinsic",
	StringPrototypeTrimIntrinsic:             "StringPrototypeTrimIntrinsic",
	StringPrototypeTrimStartIntrinsic:        "StringPrototypeTrimStartIntrinsic",
	StringPrototypeTrimEndIntrinsic:          "StringPrototypeTrimEndIntrinsic",
	SymbolPrototypeToStringIntrinsic:         "SymbolPrototypeToStringIntrinsic",
	NumberPrototypeToStringIntrinsic:         "NumberPrototypeToStringIntrinsic",
	NumberIsFiniteIntrinsic:                  "NumberIsFiniteIntrinsic",
	NumberIsNaNIntrinsic:                     "NumberIsNaNIntrinsic",
	NumberIsSafeIntegerIntrinsic:             "NumberIsSafeIntegerIntrinsic",
	NumberIsIntegerIntrinsic:                 "NumberIsIntegerIntrinsic",
	NumberConstructorIntrinsic:               "NumberConstructorIntrinsic",
	IMulIntrinsic:                            "IMulIntrinsic",
	RandomIntrinsic:                          "RandomIntrinsic",
	FRoundIntrinsic:                          "FRoundIntrinsic",
	F16RoundIntrinsic:                        "F16RoundIntrinsic",
	ToIntegerOrInfinityIntrinsic:             "ToIntegerOrInfinityIntrinsic",
	ToLengthIntrinsic:                        "ToLengthIntrinsic",
	TruncIntrinsic:                           "TruncIntrinsic",
	TypedArrayValuesIntrinsic:                "TypedArrayValuesIntrinsic",
	TypedArrayKeysIntrinsic:                  "TypedArrayKeysIntrinsic",
	TypedArrayEntriesIntrinsic:               "TypedArrayEntriesIntrinsic",
	IsTypedArrayViewIntrinsic:                "IsTypedArrayViewIntrinsic",
	ArrayBufferIsViewIntrinsic:               "ArrayBufferIsViewIntrinsic",
	BoundFunctionCallIntrinsic:               "BoundFunctionCallIntrinsic",
	RemoteFunctionCallIntrinsic:              "RemoteFunctionCallIntrinsic",
	IteratorIntrinsic:                        "IteratorIntrinsic",
	JSMapGetIntrinsic:                        "JSMapGetIntrinsic",
	JSMapHasIntrinsic:                        "JSMapHasIntrinsic",
	JSMapSetIntrinsic:                        "JSMapSetIntrinsic",
	JSMapDeleteIntrinsic:                     "JSMapDeleteIntrinsic",
	JSMapValuesIntrinsic:                     "JSMapValuesIntrinsic",
	JSMapKeysIntrinsic:                       "JSMapKeysIntrinsic",
	JSMapEntriesIntrinsic:                    "JSMapEntriesIntrinsic",
	JSMapStorageIntrinsic:                    "JSMapStorageIntrinsic",
	JSMapIterationNextIntrinsic:              "JSMapIterationNextIntrinsic",
	JSMapIterationEntryIntrinsic:             "JSMapIterationEntryIntrinsic",
	JSMapIterationEntryKeyIntrinsic:          "JSMapIterationEntryKeyIntrinsic",
	JSMapIterationEntryValueIntrinsic:        "JSMapIterationEntryValueIntrinsic",
	JSSetStorageIntrinsic:                    "JSSetStorageIntrinsic",
	JSSetIterationNextIntrinsic:              "JSSetIterationNextIntrinsic",
	JSSetIterationEntryIntrinsic:             "JSSetIterationEntryIntrinsic",
	JSSetIterationEntryKeyIntrinsic:          "JSSetIterationEntryKeyIntrinsic",
	JSMapIteratorNextIntrinsic:               "JSMapIteratorNextIntrinsic",
	JSSetIteratorNextIntrinsic:               "JSSetIteratorNextIntrinsic",
	JSSetHasIntrinsic:                        "JSSetHasIntrinsic",
	JSSetAddIntrinsic:                        "JSSetAddIntrinsic",
	JSSetDeleteIntrinsic:                     "JSSetDeleteIntrinsic",
	JSSetValuesIntrinsic:                     "JSSetValuesIntrinsic",
	JSSetEntriesIntrinsic:                    "JSSetEntriesIntrinsic",
	JSStringIteratorIntrinsic:                "JSStringIteratorIntrinsic",
	JSStringIteratorNextIntrinsic:            "JSStringIteratorNextIntrinsic",
	JSWeakMapGetIntrinsic:                    "JSWeakMapGetIntrinsic",
	JSWeakMapHasIntrinsic:                    "JSWeakMapHasIntrinsic",
	JSWeakMapSetIntrinsic:                    "JSWeakMapSetIntrinsic",
	JSWeakSetHasIntrinsic:                    "JSWeakSetHasIntrinsic",
	JSWeakSetAddIntrinsic:                    "JSWeakSetAddIntrinsic",
	HasOwnPropertyIntrinsic:                  "HasOwnPropertyIntrinsic",
	AtomicsAddIntrinsic:                      "AtomicsAddIntrinsic",
	AtomicsAndIntrinsic:                      "AtomicsAndIntrinsic",
	AtomicsCompareExchangeIntrinsic:          "AtomicsCompareExchangeIntrinsic",
	AtomicsExchangeIntrinsic:                 "AtomicsExchangeIntrinsic",
	AtomicsIsLockFreeIntrinsic:               "AtomicsIsLockFreeIntrinsic",
	AtomicsLoadIntrinsic:                     "AtomicsLoadIntrinsic",
	AtomicsNotifyIntrinsic:                   "AtomicsNotifyIntrinsic",
	AtomicsOrIntrinsic:                       "AtomicsOrIntrinsic",
	AtomicsPauseIntrinsic:                    "AtomicsPauseIntrinsic",
	AtomicsStoreIntrinsic:                    "AtomicsStoreIntrinsic",
	AtomicsSubIntrinsic:                      "AtomicsSubIntrinsic",
	AtomicsWaitIntrinsic:                     "AtomicsWaitIntrinsic",
	AtomicsWaitAsyncIntrinsic:                "AtomicsWaitAsyncIntrinsic",
	AtomicsXorIntrinsic:                      "AtomicsXorIntrinsic",
	ParseIntIntrinsic:                        "ParseIntIntrinsic",
	FunctionToStringIntrinsic:                "FunctionToStringIntrinsic",
	FunctionBindIntrinsic:                    "FunctionBindIntrinsic",
	IteratorHelperCreateIntrinsic:            "IteratorHelperCreateIntrinsic",
	WrapForValidIteratorCreateIntrinsic:      "WrapForValidIteratorCreateIntrinsic",
	RegExpStringIteratorCreateIntrinsic:      "RegExpStringIteratorCreateIntrinsic",
	RegExpStringIteratorNextIntrinsic:        "RegExpStringIteratorNextIntrinsic",
	ResolvePromiseWithFirstResolvingFunctionCallCheckIntrinsic: "ResolvePromiseWithFirstResolvingFunctionCallCheckIntrinsic",
	RejectPromiseWithFirstResolvingFunctionCallCheckIntrinsic:  "RejectPromiseWithFirstResolvingFunctionCallCheckIntrinsic",
	FulfillPromiseWithFirstResolvingFunctionCallCheckIntrinsic: "FulfillPromiseWithFirstResolvingFunctionCallCheckIntrinsic",
	NewResolvedPromiseIntrinsic:                                "NewResolvedPromiseIntrinsic",
	NewRejectedPromiseIntrinsic:                                "NewRejectedPromiseIntrinsic",
	PromiseConstructorResolveIntrinsic:                         "PromiseConstructorResolveIntrinsic",
	PromiseResolveIntrinsic:                                    "PromiseResolveIntrinsic",
	PromiseConstructorRejectIntrinsic:                          "PromiseConstructorRejectIntrinsic",
	PromiseRejectIntrinsic:                                     "PromiseRejectIntrinsic",
	PromisePrototypeThenIntrinsic:                              "PromisePrototypeThenIntrinsic",
	PromisePrototypeCatchIntrinsic:                             "PromisePrototypeCatchIntrinsic",
	TypedArrayLengthIntrinsic:                                  "TypedArrayLengthIntrinsic",
	TypedArrayByteLengthIntrinsic:                              "TypedArrayByteLengthIntrinsic",
	DataViewByteLengthIntrinsic:                                "DataViewByteLengthIntrinsic",
	TypedArrayByteOffsetIntrinsic:                              "TypedArrayByteOffsetIntrinsic",
	UnderscoreProtoIntrinsic:                                   "UnderscoreProtoIntrinsic",
	SpeciesGetterIntrinsic:                                     "SpeciesGetterIntrinsic",
	WebAssemblyInstanceExportsIntrinsic:                        "WebAssemblyInstanceExportsIntrinsic",
	JSSetSizeIntrinsic:                                         "JSSetSizeIntrinsic",
	JSMapSizeIntrinsic:                                         "JSMapSizeIntrinsic",
	RegExpHasIndicesIntrinsic:                                  "RegExpHasIndicesIntrinsic",
	RegExpGlobalIntrinsic:                                      "RegExpGlobalIntrinsic",
	RegExpIgnoreCaseIntrinsic:                                  "RegExpIgnoreCaseIntrinsic",
	RegExpMultilineIntrinsic:                                   "RegExpMultilineIntrinsic",
	RegExpDotAllIntrinsic:                                      "RegExpDotAllIntrinsic",
	RegExpUnicodeIntrinsic:                                     "RegExpUnicodeIntrinsic",
	RegExpUnicodeSetsIntrinsic:                                 "RegExpUnicodeSetsIntrinsic",
	RegExpStickyIntrinsic:                                      "RegExpStickyIntrinsic",
	DFGTrueIntrinsic:                                           "DFGTrueIntrinsic",
	FTLTrueIntrinsic:                                           "FTLTrueIntrinsic",
	OSRExitIntrinsic:                                           "OSRExitIntrinsic",
	IsFinalTierIntrinsic:                                       "IsFinalTierIntrinsic",
	SetInt32HeapPredictionIntrinsic:                            "SetInt32HeapPredictionIntrinsic",
	CheckInt32Intrinsic:                                        "CheckInt32Intrinsic",
	FiatInt52Intrinsic:                                         "FiatInt52Intrinsic",
	CPUMfenceIntrinsic:                                         "CPUMfenceIntrinsic",
	CPURdtscIntrinsic:                                          "CPURdtscIntrinsic",
	CPUCpuidIntrinsic:                                          "CPUCpuidIntrinsic",
	CPUPauseIntrinsic:                                          "CPUPauseIntrinsic",
	DataViewGetInt8:                                            "DataViewGetInt8",
	DataViewGetUint8:                                           "DataViewGetUint8",
	DataViewGetInt16:                                           "DataViewGetInt16",
	DataViewGetUint16:                                          "DataViewGetUint16",
	DataViewGetInt32:                                           "DataViewGetInt32",
	DataViewGetUint32:                                          "DataViewGetUint32",
	DataViewGetFloat16:                                         "DataViewGetFloat16",
	DataViewGetFloat32:                                         "DataViewGetFloat32",
	DataViewGetFloat64:                                         "DataViewGetFloat64",
	DataViewGetBigInt64:                                        "DataViewGetBigInt64",
	DataViewGetBigUint64:                                       "DataViewGetBigUint64",
	DataViewSetInt8:                                            "DataViewSetInt8",
	DataViewSetUint8:                                           "DataViewSetUint8",
	DataViewSetInt16:                                           "DataViewSetInt16",
	DataViewSetUint16:                                          "DataViewSetUint16",
	DataViewSetInt32:                                           "DataViewSetInt32",
	DataViewSetUint32:                                          "DataViewSetUint32",
	DataViewSetFloat16:                                         "DataViewSetFloat16",
	DataViewSetFloat32:                                         "DataViewSetFloat32",
	DataViewSetFloat64:                                         "DataViewSetFloat64",
	DataViewSetBigInt64:                                        "DataViewSetBigInt64",
	DataViewSetBigUint64:                                       "DataViewSetBigUint64",
	WasmFunctionIntrinsic:                                      "WasmFunctionIntrinsic",
}

func (i Intrinsic) String() string {
	if int(i) < len(intrinsicNames) {
		return intrinsicNames[i]
	}
	return "Intrinsic(?)"
}
