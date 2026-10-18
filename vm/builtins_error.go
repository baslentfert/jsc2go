package vm

// Error, the NativeErrors, AggregateError and SuppressedError
// (ErrorConstructor.cpp, ErrorPrototype.cpp, NativeErrorConstructor.cpp).

func (r *Realm) initErrors() {
	vm := r.vm
	for kind := ErrorKindError; kind < numberOfErrorKinds; kind++ {
		protoParent := r.ObjectPrototype
		if kind != ErrorKindError {
			protoParent = r.ErrorPrototypes[ErrorKindError]
		}
		proto := r.newObject(protoParent)
		r.ErrorPrototypes[kind] = proto
		name := kind.String()
		proto.putDirect(StrKey("name"), Str(name), BuiltinFlags)
		proto.putDirect(StrKey("message"), StringValue(emptyString), BuiltinFlags)

		length := 1
		switch kind {
		case ErrorKindAggregateError:
			length = 2
		case ErrorKindSuppressedError:
			length = 3
		}
		k := kind
		c := r.constructor(name, length, func(c *Call) Value {
			newTarget := c.NewTarget
			if newTarget == nil {
				newTarget = c.Callee
			}
			o := vm.OrdinaryCreateFromConstructor(newTarget, func(r *Realm) *Object { return r.ErrorPrototypes[k] })
			o.class = ClassError
			args := c.Args
			var errors, suppressed, errorArg Value
			switch k {
			case ErrorKindAggregateError:
				errors = c.Arg(0)
				if len(args) > 0 {
					args = args[1:]
				}
			case ErrorKindSuppressedError:
				errorArg, suppressed = c.Arg(0), c.Arg(1)
				if len(args) > 2 {
					args = args[2:]
				} else {
					args = nil
				}
			}
			msg := Undefined
			if len(args) > 0 {
				msg = args[0]
			}
			if !msg.IsUndefined() {
				o.putDirect(StrKey("message"), StringValue(vm.ToString(msg)), Writable|Configurable)
			}
			if k != ErrorKindSuppressedError && len(args) > 1 {
				if opts := args[1].AsObject(); opts != nil && opts.HasProperty(StrKey("cause")) {
					o.putDirect(StrKey("cause"), opts.GetStr("cause"), Writable|Configurable)
				}
			}
			switch k {
			case ErrorKindAggregateError:
				list := vm.iterableToList(errors)
				o.putDirect(StrKey("errors"), ObjectValue(c.Realm().NewArray(list...)), Writable|Configurable)
			case ErrorKindSuppressedError:
				o.putDirect(StrKey("error"), errorArg, Writable|Configurable)
				o.putDirect(StrKey("suppressed"), suppressed, Writable|Configurable)
			}
			vm.attachStack(o)
			return ObjectValue(o)
		}, proto)
		if kind != ErrorKindError {
			c.proto = r.ErrorConstructors[ErrorKindError]
		}
		r.ErrorConstructors[kind] = c
	}

	errorProto := r.ErrorPrototypes[ErrorKindError]
	r.method(errorProto, "toString", 0, func(c *Call) Value {
		o := c.This.AsObject()
		if o == nil {
			vm.throwTypeError("Error.prototype.toString called on non-object")
		}
		name := o.GetStr("name")
		nameStr := NewString("Error")
		if !name.IsUndefined() {
			nameStr = vm.ToString(name)
		}
		msg := o.GetStr("message")
		msgStr := emptyString
		if !msg.IsUndefined() {
			msgStr = vm.ToString(msg)
		}
		switch {
		case nameStr.Length() == 0:
			return StringValue(msgStr)
		case msgStr.Length() == 0:
			return StringValue(nameStr)
		}
		return StringValue(Concat(nameStr, Concat(NewString(": "), msgStr)))
	})
	errorCtor := r.ErrorConstructors[ErrorKindError]
	r.method(errorCtor, "captureStackTrace", 1, func(c *Call) Value {
		o := c.Arg(0).AsObject()
		if o == nil {
			vm.throwTypeError("captureStackTrace requires an object")
		}
		vm.attachStack(o)
		return Undefined
	})
	r.method(errorCtor, "isError", 1, func(c *Call) Value {
		o := c.Arg(0).AsObject()
		return Bool(o != nil && o.class == ClassError)
	})
}

// attachStack adds a "stack" property describing the current call stack.
func (vm *VM) attachStack(o *Object) {
	o.putDirect(StrKey("stack"), Str(vm.stackTrace()), Writable|Configurable)
}

// stackTrace describes the JavaScript frames on the stack.
func (vm *VM) stackTrace() string {
	var b []byte
	for f := vm.currentFrame; f != nil; f = f.caller {
		name := "global code"
		if f.callee != nil {
			if n, ok := lookupDataProperty(f.callee, StrKey("name")); ok && n.IsString() && n.StringRef().Length() > 0 {
				name = n.StringRef().GoString()
			} else {
				name = "anonymous"
			}
		}
		url := ""
		if f.cb.source.Provider != nil {
			url = f.cb.source.Provider.URL
		}
		line := 0
		if f.cb.source.Provider != nil {
			info := f.cb.u.ExpressionInfoForInstPC(uint32(f.pc))
			line = f.cb.source.Provider.LineColumnForOffset(f.cb.source.StartOffset + int(info.Divot)).Line
		}
		if len(b) > 0 {
			b = append(b, '\n')
		}
		b = append(b, name...)
		b = append(b, '@')
		b = append(b, url...)
		b = append(b, ':')
		b = appendInt(b, line)
	}
	return string(b)
}

func appendInt(b []byte, n int) []byte {
	if n >= 10 {
		b = appendInt(b, n/10)
	}
	return append(b, byte('0'+n%10))
}
