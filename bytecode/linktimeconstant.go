package bytecode

// Port of JavaScriptCore/bytecode/LinkTimeConstant.h. These are values the
// global object provides when a code block is linked. Builtin link-time
// constants (JSC_FOREACH_BUILTIN_LINK_TIME_CONSTANT) are added when the JS
// builtins are ported.

// LinkTimeConstant identifies a value supplied by the global object at link time.
type LinkTimeConstant int32

func (LinkTimeConstant) isConstant() {}

const (
	LinkTimeConstantThrowTypeErrorFunction LinkTimeConstant = iota
	LinkTimeConstantImportModule
	LinkTimeConstantMapStorage
	LinkTimeConstantMapIterationNext
	LinkTimeConstantMapIterationEntry
	LinkTimeConstantMapIterationEntryKey
	LinkTimeConstantMapIterationEntryValue
	LinkTimeConstantSetStorage
	LinkTimeConstantSetIterationNext
	LinkTimeConstantSetIterationEntry
	LinkTimeConstantSetIterationEntryKey
	LinkTimeConstantSetPrototypeDirect
	LinkTimeConstantSetPrototypeDirectOrThrow
	LinkTimeConstantCopyDataProperties
	LinkTimeConstantCloneObject
	LinkTimeConstantResolvePromise
	LinkTimeConstantRejectPromise
	LinkTimeConstantFulfillPromise
	LinkTimeConstantMarkPromiseAsHandled
	LinkTimeConstantIsPromiseStatePending
	LinkTimeConstantResolvePromiseWithFirstResolvingFunctionCallCheck
	LinkTimeConstantRejectPromiseWithFirstResolvingFunctionCallCheck
	LinkTimeConstantFulfillPromiseWithFirstResolvingFunctionCallCheck
	LinkTimeConstantNewResolvedPromise
	LinkTimeConstantNewRejectedPromise
	LinkTimeConstantResolveWithInternalMicrotaskForAsyncAwait
	LinkTimeConstantAsyncGeneratorPrototypeNext
	LinkTimeConstantAsyncIteratorPrototypeSymbolAsyncIterator
	LinkTimeConstantAsyncFunctionDrive
	LinkTimeConstantNewHandledRejectedPromise
	LinkTimeConstantPromiseReturnUndefinedOnFulfilled
	LinkTimeConstantPromiseResolve
	LinkTimeConstantPromiseReject
	LinkTimeConstantPerformPromiseThen
	LinkTimeConstantMakeTypeError
	LinkTimeConstantAggregateError
	LinkTimeConstantTypedArrayLength
	LinkTimeConstantToIntegerOrInfinity
	LinkTimeConstantToLength
	LinkTimeConstantIsTypedArrayView
	LinkTimeConstantIsSharedTypedArrayView
	LinkTimeConstantIsResizableOrGrowableSharedTypedArrayView
	LinkTimeConstantTypedArrayFromFast
	LinkTimeConstantIsDetached
	LinkTimeConstantIsTypedArrayOutOfBounds
	LinkTimeConstantIsFinite
	LinkTimeConstantInstanceOf
	LinkTimeConstantBuiltinLog
	LinkTimeConstantBuiltinDescribe
	LinkTimeConstantRegExp
	LinkTimeConstantIterator
	LinkTimeConstantMin
	LinkTimeConstantPromise
	LinkTimeConstantDefaultPromiseThen
	LinkTimeConstantRepeatCharacter
	LinkTimeConstantIsArray
	LinkTimeConstantSet
	LinkTimeConstantMap
	LinkTimeConstantImportInRealm
	LinkTimeConstantEvalFunction
	LinkTimeConstantEvalInRealm
	LinkTimeConstantMoveFunctionToRealm
	LinkTimeConstantIsConstructor
	LinkTimeConstantSameValue
	LinkTimeConstantRegExpProtoFlagsGetter
	LinkTimeConstantRegExpProtoGlobalGetter
	LinkTimeConstantRegExpProtoHasIndicesGetter
	LinkTimeConstantRegExpProtoIgnoreCaseGetter
	LinkTimeConstantRegExpProtoMultilineGetter
	LinkTimeConstantRegExpProtoSourceGetter
	LinkTimeConstantRegExpProtoStickyGetter
	LinkTimeConstantRegExpProtoDotAllGetter
	LinkTimeConstantRegExpProtoUnicodeGetter
	LinkTimeConstantRegExpProtoUnicodeSetsGetter
	LinkTimeConstantRegExpBuiltinExec
	LinkTimeConstantRegExpCreate
	LinkTimeConstantIsRegExp
	LinkTimeConstantRegExpSearchFast
	LinkTimeConstantRegExpPrototypeSymbolMatch
	LinkTimeConstantRegExpPrototypeSymbolMatchAll
	LinkTimeConstantRegExpPrototypeSymbolReplace
	LinkTimeConstantStringIncludesInternal
	LinkTimeConstantStringIndexOfInternal
	LinkTimeConstantStringSubstring
	LinkTimeConstantHandleNegativeProxyHasTrapResult
	LinkTimeConstantHandlePositiveProxySetTrapResult
	LinkTimeConstantHandleProxyGetTrapResult
	LinkTimeConstantObject
	LinkTimeConstantArray
	LinkTimeConstantApplyFunction
	LinkTimeConstantCallFunction
	LinkTimeConstantHasOwnPropertyFunction
	LinkTimeConstantReflectConstructFunction
	LinkTimeConstantCreatePrivateSymbol
	LinkTimeConstantEmptyPropertyNameEnumerator
	LinkTimeConstantSentinelString
	LinkTimeConstantCreateRemoteFunction
	LinkTimeConstantIsRemoteFunction
	LinkTimeConstantArrayFromFastWithoutMapFn
	LinkTimeConstantJsonParse
	LinkTimeConstantJsonStringify
	LinkTimeConstantString
	LinkTimeConstantInt8Array
	LinkTimeConstantUint8Array
	LinkTimeConstantUint8ClampedArray
	LinkTimeConstantInt16Array
	LinkTimeConstantUint16Array
	LinkTimeConstantInt32Array
	LinkTimeConstantUint32Array
	LinkTimeConstantFloat16Array
	LinkTimeConstantFloat32Array
	LinkTimeConstantFloat64Array
	LinkTimeConstantBigInt64Array
	LinkTimeConstantBigUint64Array
	LinkTimeConstantWrapForValidIteratorCreate
	LinkTimeConstantAsyncFromSyncIteratorCreate
	LinkTimeConstantRegExpStringIteratorCreate
	LinkTimeConstantIteratorHelperCreate
	LinkTimeConstantOwnKeys
	LinkTimeConstantReferenceError
	LinkTimeConstantSuppressedError
	LinkTimeConstantDisposableStack
	LinkTimeConstantAsyncDisposableStack

	// Builtin link-time constants (JSC_FOREACH_BUILTIN_LINK_TIME_CONSTANT) that the
	// bytecode generator refers to directly.
	LinkTimeConstantGetDisposeMethod
	LinkTimeConstantGetAsyncDisposeMethod

	NumberOfLinkTimeConstants
)

var linkTimeConstantNames = [...]string{
	LinkTimeConstantThrowTypeErrorFunction:                            "throwTypeErrorFunction",
	LinkTimeConstantImportModule:                                      "importModule",
	LinkTimeConstantMapStorage:                                        "mapStorage",
	LinkTimeConstantMapIterationNext:                                  "mapIterationNext",
	LinkTimeConstantMapIterationEntry:                                 "mapIterationEntry",
	LinkTimeConstantMapIterationEntryKey:                              "mapIterationEntryKey",
	LinkTimeConstantMapIterationEntryValue:                            "mapIterationEntryValue",
	LinkTimeConstantSetStorage:                                        "setStorage",
	LinkTimeConstantSetIterationNext:                                  "setIterationNext",
	LinkTimeConstantSetIterationEntry:                                 "setIterationEntry",
	LinkTimeConstantSetIterationEntryKey:                              "setIterationEntryKey",
	LinkTimeConstantSetPrototypeDirect:                                "setPrototypeDirect",
	LinkTimeConstantSetPrototypeDirectOrThrow:                         "setPrototypeDirectOrThrow",
	LinkTimeConstantCopyDataProperties:                                "copyDataProperties",
	LinkTimeConstantCloneObject:                                       "cloneObject",
	LinkTimeConstantResolvePromise:                                    "resolvePromise",
	LinkTimeConstantRejectPromise:                                     "rejectPromise",
	LinkTimeConstantFulfillPromise:                                    "fulfillPromise",
	LinkTimeConstantMarkPromiseAsHandled:                              "markPromiseAsHandled",
	LinkTimeConstantIsPromiseStatePending:                             "isPromiseStatePending",
	LinkTimeConstantResolvePromiseWithFirstResolvingFunctionCallCheck: "resolvePromiseWithFirstResolvingFunctionCallCheck",
	LinkTimeConstantRejectPromiseWithFirstResolvingFunctionCallCheck:  "rejectPromiseWithFirstResolvingFunctionCallCheck",
	LinkTimeConstantFulfillPromiseWithFirstResolvingFunctionCallCheck: "fulfillPromiseWithFirstResolvingFunctionCallCheck",
	LinkTimeConstantNewResolvedPromise:                                "newResolvedPromise",
	LinkTimeConstantNewRejectedPromise:                                "newRejectedPromise",
	LinkTimeConstantResolveWithInternalMicrotaskForAsyncAwait:         "resolveWithInternalMicrotaskForAsyncAwait",
	LinkTimeConstantAsyncGeneratorPrototypeNext:                       "asyncGeneratorPrototypeNext",
	LinkTimeConstantAsyncIteratorPrototypeSymbolAsyncIterator:         "asyncIteratorPrototypeSymbolAsyncIterator",
	LinkTimeConstantAsyncFunctionDrive:                                "asyncFunctionDrive",
	LinkTimeConstantNewHandledRejectedPromise:                         "newHandledRejectedPromise",
	LinkTimeConstantPromiseReturnUndefinedOnFulfilled:                 "promiseReturnUndefinedOnFulfilled",
	LinkTimeConstantPromiseResolve:                                    "promiseResolve",
	LinkTimeConstantPromiseReject:                                     "promiseReject",
	LinkTimeConstantPerformPromiseThen:                                "performPromiseThen",
	LinkTimeConstantMakeTypeError:                                     "makeTypeError",
	LinkTimeConstantAggregateError:                                    "AggregateError",
	LinkTimeConstantTypedArrayLength:                                  "typedArrayLength",
	LinkTimeConstantToIntegerOrInfinity:                               "toIntegerOrInfinity",
	LinkTimeConstantToLength:                                          "toLength",
	LinkTimeConstantIsTypedArrayView:                                  "isTypedArrayView",
	LinkTimeConstantIsSharedTypedArrayView:                            "isSharedTypedArrayView",
	LinkTimeConstantIsResizableOrGrowableSharedTypedArrayView:         "isResizableOrGrowableSharedTypedArrayView",
	LinkTimeConstantTypedArrayFromFast:                                "typedArrayFromFast",
	LinkTimeConstantIsDetached:                                        "isDetached",
	LinkTimeConstantIsTypedArrayOutOfBounds:                           "isTypedArrayOutOfBounds",
	LinkTimeConstantIsFinite:                                          "isFinite",
	LinkTimeConstantInstanceOf:                                        "instanceOf",
	LinkTimeConstantBuiltinLog:                                        "BuiltinLog",
	LinkTimeConstantBuiltinDescribe:                                   "BuiltinDescribe",
	LinkTimeConstantRegExp:                                            "RegExp",
	LinkTimeConstantIterator:                                          "Iterator",
	LinkTimeConstantMin:                                               "min",
	LinkTimeConstantPromise:                                           "Promise",
	LinkTimeConstantDefaultPromiseThen:                                "defaultPromiseThen",
	LinkTimeConstantRepeatCharacter:                                   "repeatCharacter",
	LinkTimeConstantIsArray:                                           "isArray",
	LinkTimeConstantSet:                                               "Set",
	LinkTimeConstantMap:                                               "Map",
	LinkTimeConstantImportInRealm:                                     "importInRealm",
	LinkTimeConstantEvalFunction:                                      "evalFunction",
	LinkTimeConstantEvalInRealm:                                       "evalInRealm",
	LinkTimeConstantMoveFunctionToRealm:                               "moveFunctionToRealm",
	LinkTimeConstantIsConstructor:                                     "isConstructor",
	LinkTimeConstantSameValue:                                         "sameValue",
	LinkTimeConstantRegExpProtoFlagsGetter:                            "regExpProtoFlagsGetter",
	LinkTimeConstantRegExpProtoGlobalGetter:                           "regExpProtoGlobalGetter",
	LinkTimeConstantRegExpProtoHasIndicesGetter:                       "regExpProtoHasIndicesGetter",
	LinkTimeConstantRegExpProtoIgnoreCaseGetter:                       "regExpProtoIgnoreCaseGetter",
	LinkTimeConstantRegExpProtoMultilineGetter:                        "regExpProtoMultilineGetter",
	LinkTimeConstantRegExpProtoSourceGetter:                           "regExpProtoSourceGetter",
	LinkTimeConstantRegExpProtoStickyGetter:                           "regExpProtoStickyGetter",
	LinkTimeConstantRegExpProtoDotAllGetter:                           "regExpProtoDotAllGetter",
	LinkTimeConstantRegExpProtoUnicodeGetter:                          "regExpProtoUnicodeGetter",
	LinkTimeConstantRegExpProtoUnicodeSetsGetter:                      "regExpProtoUnicodeSetsGetter",
	LinkTimeConstantRegExpBuiltinExec:                                 "regExpBuiltinExec",
	LinkTimeConstantRegExpCreate:                                      "regExpCreate",
	LinkTimeConstantIsRegExp:                                          "isRegExp",
	LinkTimeConstantRegExpSearchFast:                                  "regExpSearchFast",
	LinkTimeConstantRegExpPrototypeSymbolMatch:                        "regExpPrototypeSymbolMatch",
	LinkTimeConstantRegExpPrototypeSymbolMatchAll:                     "regExpPrototypeSymbolMatchAll",
	LinkTimeConstantRegExpPrototypeSymbolReplace:                      "regExpPrototypeSymbolReplace",
	LinkTimeConstantStringIncludesInternal:                            "stringIncludesInternal",
	LinkTimeConstantStringIndexOfInternal:                             "stringIndexOfInternal",
	LinkTimeConstantStringSubstring:                                   "stringSubstring",
	LinkTimeConstantHandleNegativeProxyHasTrapResult:                  "handleNegativeProxyHasTrapResult",
	LinkTimeConstantHandlePositiveProxySetTrapResult:                  "handlePositiveProxySetTrapResult",
	LinkTimeConstantHandleProxyGetTrapResult:                          "handleProxyGetTrapResult",
	LinkTimeConstantObject:                                            "Object",
	LinkTimeConstantArray:                                             "Array",
	LinkTimeConstantApplyFunction:                                     "applyFunction",
	LinkTimeConstantCallFunction:                                      "callFunction",
	LinkTimeConstantHasOwnPropertyFunction:                            "hasOwnPropertyFunction",
	LinkTimeConstantReflectConstructFunction:                          "reflectConstructFunction",
	LinkTimeConstantCreatePrivateSymbol:                               "createPrivateSymbol",
	LinkTimeConstantEmptyPropertyNameEnumerator:                       "emptyPropertyNameEnumerator",
	LinkTimeConstantSentinelString:                                    "sentinelString",
	LinkTimeConstantCreateRemoteFunction:                              "createRemoteFunction",
	LinkTimeConstantIsRemoteFunction:                                  "isRemoteFunction",
	LinkTimeConstantArrayFromFastWithoutMapFn:                         "arrayFromFastWithoutMapFn",
	LinkTimeConstantJsonParse:                                         "jsonParse",
	LinkTimeConstantJsonStringify:                                     "jsonStringify",
	LinkTimeConstantString:                                            "String",
	LinkTimeConstantInt8Array:                                         "Int8Array",
	LinkTimeConstantUint8Array:                                        "Uint8Array",
	LinkTimeConstantUint8ClampedArray:                                 "Uint8ClampedArray",
	LinkTimeConstantInt16Array:                                        "Int16Array",
	LinkTimeConstantUint16Array:                                       "Uint16Array",
	LinkTimeConstantInt32Array:                                        "Int32Array",
	LinkTimeConstantUint32Array:                                       "Uint32Array",
	LinkTimeConstantFloat16Array:                                      "Float16Array",
	LinkTimeConstantFloat32Array:                                      "Float32Array",
	LinkTimeConstantFloat64Array:                                      "Float64Array",
	LinkTimeConstantBigInt64Array:                                     "BigInt64Array",
	LinkTimeConstantBigUint64Array:                                    "BigUint64Array",
	LinkTimeConstantWrapForValidIteratorCreate:                        "wrapForValidIteratorCreate",
	LinkTimeConstantAsyncFromSyncIteratorCreate:                       "asyncFromSyncIteratorCreate",
	LinkTimeConstantRegExpStringIteratorCreate:                        "regExpStringIteratorCreate",
	LinkTimeConstantIteratorHelperCreate:                              "iteratorHelperCreate",
	LinkTimeConstantOwnKeys:                                           "ownKeys",
	LinkTimeConstantReferenceError:                                    "ReferenceError",
	LinkTimeConstantSuppressedError:                                   "SuppressedError",
	LinkTimeConstantDisposableStack:                                   "DisposableStack",
	LinkTimeConstantAsyncDisposableStack:                              "AsyncDisposableStack",
	LinkTimeConstantGetDisposeMethod:                                  "getDisposeMethod",
	LinkTimeConstantGetAsyncDisposeMethod:                             "getAsyncDisposeMethod",
}

func (c LinkTimeConstant) String() string {
	if c >= 0 && int(c) < len(linkTimeConstantNames) {
		return linkTimeConstantNames[c]
	}
	return "unknown link time constant"
}
