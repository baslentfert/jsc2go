package vm

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Date (DateConstructor.cpp, DatePrototype.cpp, JSDateMath.cpp; ECMA-262
// 21.4). Time values are milliseconds since the epoch as float64; local
// time uses the host time zone (time.Local).

const (
	msPerSecond = 1000.0
	msPerMinute = 60000.0
	msPerHour   = 3600000.0
	msPerDay    = 86400000.0
	maxTime     = 8.64e15
)

type dateData struct{ t float64 }

func day(t float64) float64           { return math.Floor(t / msPerDay) }
func timeWithinDay(t float64) float64 { return posMod(t, msPerDay) }

func posMod(a, b float64) float64 {
	// Time values are integers below 2^53: math.Mod is much slower.
	if a > -1<<62 && a < 1<<62 && b > 0 && b < 1<<62 {
		if ia, ib := int64(a), int64(b); float64(ia) == a && float64(ib) == b {
			r := ia % ib
			if r < 0 {
				r += ib
			}
			return float64(r)
		}
	}
	r := math.Mod(a, b)
	if r < 0 {
		r += b
	}
	return r + 0 // math.Mod keeps the sign of a: turn -0 into +0
}

func daysInYear(y float64) float64 {
	if y > -1<<62 && y < 1<<62 && float64(int64(y)) == y {
		if iy := int64(y); iy%4 != 0 || iy%100 == 0 && iy%400 != 0 {
			return 365
		}
		return 366
	}
	switch {
	case math.Mod(y, 4) != 0:
		return 365
	case math.Mod(y, 100) != 0:
		return 366
	case math.Mod(y, 400) != 0:
		return 365
	}
	return 366
}

func dayFromYear(y float64) float64 {
	return 365*(y-1970) + math.Floor((y-1969)/4) - math.Floor((y-1901)/100) + math.Floor((y-1601)/400)
}

func timeFromYear(y float64) float64 { return msPerDay * dayFromYear(y) }

func yearFromTime(t float64) float64 {
	y := math.Floor(t/(msPerDay*365.2425)) + 1970
	for timeFromYear(y) > t {
		y--
	}
	for timeFromYear(y+1) <= t {
		y++
	}
	return y
}

func inLeapYear(t float64) bool { return daysInYear(yearFromTime(t)) == 366 }

func dayWithinYear(t float64) float64 { return day(t) - dayFromYear(yearFromTime(t)) }

var cumulativeDays = [2][13]float64{
	{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334, 365},
	{0, 31, 60, 91, 121, 152, 182, 213, 244, 274, 305, 335, 366},
}

func monthFromTime(t float64) float64 {
	d := dayWithinYear(t)
	leap := 0
	if inLeapYear(t) {
		leap = 1
	}
	for m := 0; m < 12; m++ {
		if d < cumulativeDays[leap][m+1] {
			return float64(m)
		}
	}
	return 11
}

func dateFromTime(t float64) float64 {
	leap := 0
	if inLeapYear(t) {
		leap = 1
	}
	return dayWithinYear(t) - cumulativeDays[leap][int(monthFromTime(t))] + 1
}

func weekDay(t float64) float64      { return posMod(day(t)+4, 7) }
func hourFromTime(t float64) float64 { return posMod(math.Floor(t/msPerHour), 24) }
func minFromTime(t float64) float64  { return posMod(math.Floor(t/msPerMinute), 60) }
func secFromTime(t float64) float64  { return posMod(math.Floor(t/msPerSecond), 60) }
func msFromTime(t float64) float64   { return posMod(t, msPerSecond) }

func isFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// makeTime (21.4.1.27).
func makeTime(h, m, s, ms float64) float64 {
	if !isFinite(h) || !isFinite(m) || !isFinite(s) || !isFinite(ms) {
		return math.NaN()
	}
	// The float64 conversions round each product, so that no platform fuses
	// them into the additions (FMA), as the spec's IEEE * and + require.
	return float64(float64(math.Trunc(h)*msPerHour)+float64(math.Trunc(m)*msPerMinute)) + float64(math.Trunc(s)*msPerSecond) + math.Trunc(ms)
}

// makeDay (21.4.1.28).
func makeDay(year, month, date float64) float64 {
	if !isFinite(year) || !isFinite(month) || !isFinite(date) {
		return math.NaN()
	}
	y, m, dt := math.Trunc(year), math.Trunc(month), math.Trunc(date)
	ym := y + math.Floor(m/12)
	if math.Abs(ym) > 400000 {
		return math.NaN()
	}
	mn := posMod(m, 12)
	leap := 0
	if daysInYear(ym) == 366 {
		leap = 1
	}
	return dayFromYear(ym) + cumulativeDays[leap][int(mn)] + dt - 1
}

func makeDate(day, t float64) float64 {
	if !isFinite(day) || !isFinite(t) {
		return math.NaN()
	}
	tv := float64(day*msPerDay) + t // no FMA, see makeTime
	if !isFinite(tv) {
		return math.NaN()
	}
	return tv
}

func timeClip(t float64) float64 {
	if !isFinite(t) || math.Abs(t) > maxTime {
		return math.NaN()
	}
	return math.Trunc(t) + 0 // +0 normalizes -0
}

// localOffset returns the offset of the host time zone at UTC time t (ms).
// zoneSpan is a period in which the host time zone has one UTC offset.
type zoneSpan struct {
	start, end int64 // Unix seconds, end exclusive
	offset     int   // seconds east of UTC
}

// lastZoneSpan caches the span of the last lookup (JSC's
// LocalTimeOffsetCache): dates in a program tend to be close together.
var lastZoneSpan atomic.Pointer[zoneSpan]

// zoneOffset is the host time zone's UTC offset in seconds at Unix time secs.
func zoneOffset(secs int64) int {
	if z := lastZoneSpan.Load(); z != nil && secs >= z.start && secs < z.end {
		return z.offset
	}
	tm := time.Unix(secs, 0).In(time.Local)
	_, off := tm.Zone()
	start, end := tm.ZoneBounds()
	z := &zoneSpan{start: math.MinInt64, end: math.MaxInt64, offset: off}
	if !start.IsZero() {
		z.start = start.Unix()
	}
	if !end.IsZero() {
		z.end = end.Unix()
	}
	lastZoneSpan.Store(z)
	return off
}

func localOffset(t float64, isUTC bool) float64 {
	if !isFinite(t) {
		return 0
	}
	secs := int64(math.Floor(t / 1000))
	if isUTC {
		return float64(zoneOffset(secs)) * 1000
	}
	// t is local time: find the offset that maps back to it.
	off := zoneOffset(secs)
	return float64(zoneOffset(secs-int64(off))) * 1000
}

func localTime(t float64) float64 { return t + localOffset(t, true) }
func utcTime(t float64) float64   { return t - localOffset(t, false) }

func tzName(t float64) string {
	secs := int64(math.Floor(t / 1000))
	name, _ := time.Unix(secs, 0).In(time.Local).Zone()
	return name
}

var weekdayNames = [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
var monthNames = [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

func formatYear(y float64) string {
	if y < 0 {
		return fmt.Sprintf("-%04d", int64(-y))
	}
	return fmt.Sprintf("%04d", int64(y))
}

func dateString(tv float64) string {
	return fmt.Sprintf("%s %s %02d %s", weekdayNames[int(weekDay(tv))], monthNames[int(monthFromTime(tv))],
		int(dateFromTime(tv)), formatYear(yearFromTime(tv)))
}

func timeString(tv float64) string {
	return fmt.Sprintf("%02d:%02d:%02d GMT", int(hourFromTime(tv)), int(minFromTime(tv)), int(secFromTime(tv)))
}

func timeZoneString(t float64) string {
	off := localOffset(t, true) / msPerMinute
	sign := "+"
	if off < 0 {
		sign = "-"
		off = -off
	}
	return fmt.Sprintf("%s%02d%02d (%s)", sign, int(off)/60, int(off)%60, tzName(t))
}

func toDateString(t float64) string {
	if math.IsNaN(t) {
		return "Invalid Date"
	}
	lt := localTime(t)
	return dateString(lt) + " " + timeString(lt) + timeZoneString(t)
}

func toISOString(t float64) string {
	y := yearFromTime(t)
	var ys string
	if y >= 0 && y <= 9999 {
		ys = fmt.Sprintf("%04d", int64(y))
	} else if y < 0 {
		ys = fmt.Sprintf("-%06d", int64(-y))
	} else {
		ys = fmt.Sprintf("+%06d", int64(y))
	}
	return fmt.Sprintf("%s-%02d-%02dT%02d:%02d:%02d.%03dZ", ys, int(monthFromTime(t))+1, int(dateFromTime(t)),
		int(hourFromTime(t)), int(minFromTime(t)), int(secFromTime(t)), int(msFromTime(t)))
}

func (r *Realm) initDate() {
	vm := r.vm
	proto := r.NewObject()
	r.DatePrototype = proto
	now := func() float64 { return float64(time.Now().UnixMilli()) }

	c := r.constructor("Date", 7, func(c *Call) Value {
		if c.NewTarget == nil {
			return Str(toDateString(now()))
		}
		var tv float64
		switch n := len(c.Args); {
		case n == 0:
			tv = now()
		case n == 1:
			v := c.Args[0]
			if o := v.AsObject(); o != nil && o.class == ClassDate {
				tv = o.internal.(*dateData).t
			} else {
				p := vm.ToPrimitive(v, HintDefault)
				if p.IsString() {
					tv = parseDate(p.StringRef().GoString())
				} else {
					tv = vm.ToNumber(p)
				}
			}
			tv = timeClip(tv)
		default:
			tv = timeClip(utcTime(vm.dateFromComponents(c.Args)))
		}
		o := vm.OrdinaryCreateFromConstructor(c.NewTarget, func(r *Realm) *Object { return r.DatePrototype })
		o.class = ClassDate
		o.internal = &dateData{tv}
		return ObjectValue(o)
	}, proto)
	r.method(c, "now", 0, func(c *Call) Value { return Number(now()) })
	r.method(c, "parse", 1, func(c *Call) Value {
		return Number(parseDate(vm.ToString(c.Arg(0)).GoString()))
	})
	r.method(c, "UTC", 7, func(c *Call) Value {
		return Number(timeClip(vm.dateFromComponents(c.Args)))
	})

	thisTime := func(c *Call, name string) *dateData {
		if o := c.This.AsObject(); o != nil && o.class == ClassDate {
			return o.internal.(*dateData)
		}
		vm.throwTypeError("Date.prototype.%s called on incompatible receiver", name)
		return nil
	}
	getter := func(name string, local bool, f func(t float64) float64) {
		get := func(t float64) Value {
			if math.IsNaN(t) {
				return NaN
			}
			if local {
				t = localTime(t)
			}
			return Number(f(t))
		}
		setFast(r.method(proto, name, 0, func(c *Call) Value { return get(thisTime(c, name).t) }),
			func(vm *VM, this Value, args []Value) (Value, bool) {
				if o := this.AsObject(); o != nil && o.class == ClassDate {
					return get(o.internal.(*dateData).t), true
				}
				return Undefined, false
			})
	}
	getter("getDate", true, dateFromTime)
	getter("getDay", true, weekDay)
	getter("getFullYear", true, yearFromTime)
	getter("getHours", true, hourFromTime)
	getter("getMilliseconds", true, msFromTime)
	getter("getMinutes", true, minFromTime)
	getter("getMonth", true, monthFromTime)
	getter("getSeconds", true, secFromTime)
	getter("getTime", false, func(t float64) float64 { return t })
	getter("valueOf", false, func(t float64) float64 { return t })
	getter("getTimezoneOffset", false, func(t float64) float64 { return (t - localTime(t)) / msPerMinute })
	getter("getUTCDate", false, dateFromTime)
	getter("getUTCDay", false, weekDay)
	getter("getUTCFullYear", false, yearFromTime)
	getter("getUTCHours", false, hourFromTime)
	getter("getUTCMilliseconds", false, msFromTime)
	getter("getUTCMinutes", false, minFromTime)
	getter("getUTCMonth", false, monthFromTime)
	getter("getUTCSeconds", false, secFromTime)
	getter("getYear", true, func(t float64) float64 { return yearFromTime(t) - 1900 })

	r.method(proto, "setTime", 1, func(c *Call) Value {
		d := thisTime(c, "setTime")
		d.t = timeClip(vm.ToNumber(c.Arg(0)))
		return Number(d.t)
	})

	// Setters read their arguments in order, then compute from the
	// (possibly NaN) time value.
	type setterSpec struct {
		name   string
		length int
		local  bool
		// apply builds the new time from t and the converted arguments
		apply func(t float64, args []float64, given int) float64
		// nanStart: for setFullYear/setYear a NaN time value starts at +0
		nanStart bool
	}
	argOr := func(args []float64, given, i int, def float64) float64 {
		if i < given {
			return args[i]
		}
		return def
	}
	setters := []setterSpec{
		{"setMilliseconds", 1, true, func(t float64, a []float64, n int) float64 {
			return makeDate(day(t), makeTime(hourFromTime(t), minFromTime(t), secFromTime(t), a[0]))
		}, false},
		{"setSeconds", 2, true, func(t float64, a []float64, n int) float64 {
			return makeDate(day(t), makeTime(hourFromTime(t), minFromTime(t), a[0], argOr(a, n, 1, msFromTime(t))))
		}, false},
		{"setMinutes", 3, true, func(t float64, a []float64, n int) float64 {
			return makeDate(day(t), makeTime(hourFromTime(t), a[0], argOr(a, n, 1, secFromTime(t)), argOr(a, n, 2, msFromTime(t))))
		}, false},
		{"setHours", 4, true, func(t float64, a []float64, n int) float64 {
			return makeDate(day(t), makeTime(a[0], argOr(a, n, 1, minFromTime(t)), argOr(a, n, 2, secFromTime(t)), argOr(a, n, 3, msFromTime(t))))
		}, false},
		{"setDate", 1, true, func(t float64, a []float64, n int) float64 {
			return makeDate(makeDay(yearFromTime(t), monthFromTime(t), a[0]), timeWithinDay(t))
		}, false},
		{"setMonth", 2, true, func(t float64, a []float64, n int) float64 {
			return makeDate(makeDay(yearFromTime(t), a[0], argOr(a, n, 1, dateFromTime(t))), timeWithinDay(t))
		}, false},
		{"setFullYear", 3, true, func(t float64, a []float64, n int) float64 {
			return makeDate(makeDay(a[0], argOr(a, n, 1, monthFromTime(t)), argOr(a, n, 2, dateFromTime(t))), timeWithinDay(t))
		}, true},
	}
	for _, s := range setters {
		for _, utc := range []bool{false, true} {
			s := s
			name := s.name
			if utc {
				name = "setUTC" + strings.TrimPrefix(s.name, "set")
			}
			r.method(proto, name, s.length, func(c *Call) Value {
				d := thisTime(c, name)
				t := d.t
				given := min(len(c.Args), s.length)
				if given == 0 {
					given = 1
				}
				args := make([]float64, s.length)
				for i := 0; i < given; i++ {
					args[i] = vm.ToNumber(c.Arg(i))
				}
				if math.IsNaN(t) {
					if !s.nanStart {
						return NaN
					}
					t = 0
				} else if !utc {
					t = localTime(t)
				}
				nt := s.apply(t, args, given)
				if !utc {
					nt = utcTime(nt)
				}
				d.t = timeClip(nt)
				return Number(d.t)
			})
		}
	}
	r.method(proto, "setYear", 1, func(c *Call) Value {
		d := thisTime(c, "setYear")
		t := d.t
		if math.IsNaN(t) {
			t = 0
		} else {
			t = localTime(t)
		}
		y := vm.ToNumber(c.Arg(0))
		if math.IsNaN(y) {
			d.t = math.NaN()
			return NaN
		}
		yi := integerOrInfinity(y)
		if yi >= 0 && yi <= 99 {
			yi += 1900
		}
		dd := makeDay(yi, monthFromTime(t), dateFromTime(t))
		d.t = timeClip(utcTime(makeDate(dd, timeWithinDay(t))))
		return Number(d.t)
	})

	strMethod := func(name string, f func(t float64) string) *Object {
		return r.method(proto, name, 0, func(c *Call) Value {
			t := thisTime(c, name).t
			if math.IsNaN(t) {
				return Str("Invalid Date")
			}
			return Str(f(t))
		})
	}
	strMethod("toString", toDateString)
	strMethod("toDateString", func(t float64) string { return dateString(localTime(t)) })
	strMethod("toTimeString", func(t float64) string { return timeString(localTime(t)) + timeZoneString(t) })
	strMethod("toLocaleString", toDateString)
	strMethod("toLocaleDateString", func(t float64) string { return dateString(localTime(t)) })
	strMethod("toLocaleTimeString", func(t float64) string { return timeString(localTime(t)) + timeZoneString(t) })
	utcString := strMethod("toUTCString", func(t float64) string {
		return fmt.Sprintf("%s, %02d %s %s %s", weekdayNames[int(weekDay(t))], int(dateFromTime(t)),
			monthNames[int(monthFromTime(t))], formatYear(yearFromTime(t)), timeString(t))
	})
	proto.putDirect(StrKey("toGMTString"), ObjectValue(utcString), BuiltinFlags)
	r.method(proto, "toISOString", 0, func(c *Call) Value {
		t := thisTime(c, "toISOString").t
		if math.IsNaN(t) {
			vm.throwRangeError("Invalid Date")
		}
		return Str(toISOString(t))
	})
	r.method(proto, "toJSON", 1, func(c *Call) Value {
		o := vm.ToObject(c.This)
		tv := vm.ToPrimitive(ObjectValue(o), HintNumber)
		if tv.IsNumber() && !isFinite(tv.Float()) {
			return Null
		}
		return vm.Invoke(ObjectValue(o), StrKey("toISOString"))
	})
	r.symbolMethod(proto, SymbolToPrimitive, 1, func(c *Call) Value {
		o := c.This.AsObject()
		if o == nil {
			vm.throwTypeError("Date.prototype[Symbol.toPrimitive] requires an object")
		}
		hint := c.Arg(0)
		if !hint.IsString() {
			vm.throwTypeError("Invalid hint")
		}
		switch hint.StringRef().GoString() {
		case "string", "default":
			return vm.OrdinaryToPrimitive(o, HintString)
		case "number":
			return vm.OrdinaryToPrimitive(o, HintNumber)
		}
		vm.throwTypeError("Invalid hint")
		return Undefined
	}, Configurable)
}

// dateFromComponents converts Date(y, m, ...) arguments to a time value
// (without the UTC adjustment).
func (vm *VM) dateFromComponents(args []Value) float64 {
	nums := [7]float64{math.NaN(), 0, 1, 0, 0, 0, 0}
	for i := 0; i < len(args) && i < 7; i++ {
		nums[i] = vm.ToNumber(args[i])
	}
	y := nums[0]
	if !math.IsNaN(y) {
		yi := math.Trunc(y)
		if yi >= 0 && yi <= 99 {
			y = 1900 + yi
		}
	}
	return makeDate(makeDay(y, nums[1], nums[2]), makeTime(nums[3], nums[4], nums[5], nums[6]))
}

// parseDate parses the Date Time String Format (21.4.1.32) and the
// toString/toUTCString formats.
func parseDate(s string) float64 {
	s = strings.TrimSpace(s)
	if t, ok := parseISODate(s); ok {
		return t
	}
	if t, ok := parseLooseISODate(s); ok {
		return t
	}
	return parseLegacyDate(s)
}

func parseISODate(s string) (float64, bool) {
	p := 0
	num := func(n int) (int, bool) {
		if p+n > len(s) {
			return 0, false
		}
		v := 0
		for i := 0; i < n; i++ {
			c := s[p+i]
			if c < '0' || c > '9' {
				return 0, false
			}
			v = v*10 + int(c-'0')
		}
		p += n
		return v, true
	}
	sign := 1
	var year int
	var ok bool
	if p < len(s) && (s[p] == '+' || s[p] == '-') {
		if s[p] == '-' {
			sign = -1
		}
		p++
		if year, ok = num(6); !ok {
			return 0, false
		}
		if sign == -1 && year == 0 {
			return 0, false // -000000 is invalid
		}
	} else if year, ok = num(4); !ok {
		return 0, false
	}
	month, dayN := 1, 1
	if p < len(s) && s[p] == '-' {
		p++
		if month, ok = num(2); !ok || month < 1 || month > 12 {
			return 0, false
		}
		if p < len(s) && s[p] == '-' {
			p++
			if dayN, ok = num(2); !ok || dayN < 1 || dayN > 31 {
				return 0, false
			}
		}
	}
	hour, minute, sec, ms := 0, 0, 0, 0
	dateOnly := true
	hasTZ := false
	tzOffset := 0
	if p < len(s) && (s[p] == 'T' || s[p] == 't') {
		dateOnly = false
		p++
		if hour, ok = num(2); !ok || hour > 24 {
			return 0, false
		}
		if p >= len(s) || s[p] != ':' {
			return 0, false
		}
		p++
		if minute, ok = num(2); !ok || minute > 59 {
			return 0, false
		}
		if p < len(s) && s[p] == ':' {
			p++
			if sec, ok = num(2); !ok || sec > 59 {
				return 0, false
			}
			if p < len(s) && (s[p] == '.' || s[p] == ',') {
				p++
				start := p
				frac := 0
				digits := 0
				for p < len(s) && s[p] >= '0' && s[p] <= '9' {
					if digits < 3 {
						frac = frac*10 + int(s[p]-'0')
						digits++
					}
					p++
				}
				if p == start {
					return 0, false
				}
				for digits < 3 {
					frac *= 10
					digits++
				}
				ms = frac
			}
		}
		if hour == 24 && (minute != 0 || sec != 0 || ms != 0) {
			return 0, false
		}
		if p < len(s) {
			switch s[p] {
			case 'Z', 'z':
				hasTZ = true
				p++
			case '+', '-':
				tzSign := 1
				if s[p] == '-' {
					tzSign = -1
				}
				p++
				th, ok1 := num(2)
				if !ok1 || p >= len(s) || s[p] != ':' {
					return 0, false
				}
				p++
				tm, ok2 := num(2)
				if !ok2 || th > 23 || tm > 59 {
					return 0, false
				}
				hasTZ = true
				tzOffset = tzSign * (th*60 + tm)
			}
		}
	}
	if p != len(s) {
		return 0, false
	}
	t := makeDate(makeDay(float64(sign*year), float64(month-1), float64(dayN)),
		makeTime(float64(hour), float64(minute), float64(sec), float64(ms)))
	if !hasTZ && !dateOnly {
		t = utcTime(t)
	}
	t -= float64(tzOffset) * msPerMinute
	return timeClip(t), true
}

// parseLegacyDate accepts the formats Date.prototype.toString and
// toUTCString produce, plus common variants.
func parseLegacyDate(s string) float64 {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' })
	var year, month, dayN = math.NaN(), -1.0, math.NaN()
	h, m, sec := 0.0, 0.0, 0.0
	utc := false
	offset := 0.0
	hasOffset := false
	yearDigits := 0 // digits of the year field, for the two-digit heuristic
	for _, f := range fields {
		lf := strings.ToLower(f)
		switch {
		case strings.Contains(f, "/") && !strings.Contains(f, ":"):
			// m/d/y, m/d or y/m/d.
			if month >= 0 || !math.IsNaN(dayN) {
				return math.NaN()
			}
			parts := strings.Split(f, "/")
			nums := make([]int, len(parts))
			for i, p := range parts {
				n, err := strconv.Atoi(p)
				if err != nil || n < 0 || len(parts) > 3 || len(parts) < 2 {
					return math.NaN()
				}
				nums[i] = n
			}
			switch {
			case len(parts) == 3 && (nums[0] > 31 || len(parts[0]) >= 3):
				year, month, dayN = float64(nums[0]), float64(nums[1]-1), float64(nums[2])
				yearDigits = len(parts[0])
			case len(parts) == 3:
				month, dayN, year = float64(nums[0]-1), float64(nums[1]), float64(nums[2])
				yearDigits = len(parts[2])
			default:
				month, dayN = float64(nums[0]-1), float64(nums[1])
			}
			if month < 0 || month > 11 {
				return math.NaN()
			}
		case len(f) >= 3 && monthIndex(lf[:3]) >= 0 && month < 0 && !strings.Contains(f, ":"):
			month = float64(monthIndex(lf[:3]))
		case len(f) == 3 && weekdayIndex(lf) >= 0:
		case strings.Contains(f, ":"):
			parts := strings.Split(f, ":")
			if len(parts) < 2 {
				return math.NaN()
			}
			hv, err1 := strconv.Atoi(parts[0])
			mv, err2 := strconv.Atoi(parts[1])
			if err1 != nil || err2 != nil {
				return math.NaN()
			}
			h, m = float64(hv), float64(mv)
			if len(parts) > 2 {
				sv, err := strconv.Atoi(parts[2])
				if err != nil {
					return math.NaN()
				}
				sec = float64(sv)
			}
		case strings.HasPrefix(lf, "gmt") || strings.HasPrefix(lf, "utc") || lf == "z":
			utc = true
			rest := f[3:]
			if lf == "z" {
				rest = ""
			}
			if len(rest) >= 5 && (rest[0] == '+' || rest[0] == '-') {
				hh, _ := strconv.Atoi(rest[1:3])
				mm, _ := strconv.Atoi(rest[3:5])
				offset = float64(hh*60 + mm)
				if rest[0] == '-' {
					offset = -offset
				}
				hasOffset = true
			}
		case (f[0] == '+' || f[0] == '-') && len(f) == 5:
			hh, err1 := strconv.Atoi(f[1:3])
			mm, err2 := strconv.Atoi(f[3:5])
			if err1 != nil || err2 != nil {
				return math.NaN()
			}
			offset = float64(hh*60 + mm)
			if f[0] == '-' {
				offset = -offset
			}
			hasOffset, utc = true, true
		case f[0] == '(':
			// time zone name
		default:
			n, err := strconv.Atoi(f)
			if err != nil {
				if strings.HasPrefix(f, "(") || strings.HasSuffix(f, ")") {
					continue
				}
				return math.NaN()
			}
			switch {
			case (len(f) >= 3 || n > 31) && math.IsNaN(year):
				year = float64(n)
				yearDigits = len(strings.TrimLeft(f, "+-"))
				if f[0] == '-' {
					year = float64(n)
				}
			case math.IsNaN(dayN):
				dayN = float64(n)
			case math.IsNaN(year):
				year = float64(n)
				yearDigits = len(strings.TrimLeft(f, "+-"))
			default:
				return math.NaN()
			}
		}
	}
	if math.IsNaN(year) || month < 0 || math.IsNaN(dayN) || dayN < 1 || dayN > 31 {
		return math.NaN()
	}
	if yearDigits <= 2 && year >= 0 && year < 100 {
		// Two-digit years (JSC's and SpiderMonkey's heuristic).
		if year < 50 {
			year += 2000
		} else {
			year += 1900
		}
	}
	t := makeDate(makeDay(year, month, dayN), makeTime(h, m, sec, 0))
	if utc {
		if hasOffset {
			t -= offset * msPerMinute
		}
	} else {
		t = utcTime(t)
	}
	return timeClip(t)
}

func monthIndex(s string) int {
	for i, m := range monthNames {
		if strings.ToLower(m) == s {
			return i
		}
	}
	return -1
}

func weekdayIndex(s string) int {
	for i, d := range weekdayNames {
		if strings.ToLower(d) == s {
			return i
		}
	}
	return -1
}

// looseISODate matches the ISO-like forms browsers accept besides the
// strict format (SpiderMonkey compatible): one-digit fields with a space
// instead of T (with T the strict format applies), local time unless
// a zone is given ("1997-3-8 1:1:1.01", "+001997-03-08 11:19").
var looseISODate = regexp.MustCompile(`^([+-]\d{6}|\d{4})-(\d{1,2})-(\d{1,2})(?: (\d{1,2}):(\d{1,2})(?::(\d{1,2})(?:\.(\d+))?)?)?\s*(Z|[+-]\d{2}(?::?\d{2})?)?$`)

func parseLooseISODate(s string) (float64, bool) {
	m := looseISODate.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	num := func(i int) float64 {
		if m[i] == "" {
			return 0
		}
		v, _ := strconv.ParseFloat(m[i], 64)
		return v
	}
	year, month, day := num(1), num(2), num(3)
	if m[1] == "-000000" || month < 1 || month > 12 || day < 1 || day > 31 {
		return 0, false
	}
	h, mi, sec := num(4), num(5), num(6)
	if h > 24 || mi > 59 || sec > 59 || h == 24 && (mi != 0 || sec != 0) {
		return 0, false
	}
	ms := 0.0
	if frac := m[7]; frac != "" {
		frac = (frac + "00")[:3]
		ms, _ = strconv.ParseFloat(frac, 64)
	}
	t := makeDate(makeDay(year, month-1, day), makeTime(h, mi, sec, ms))
	switch zone := m[8]; {
	case zone == "Z":
	case zone != "":
		digits := strings.ReplaceAll(zone[1:], ":", "")
		zh, _ := strconv.Atoi(digits[:2])
		zm := 0
		if len(digits) > 2 {
			zm, _ = strconv.Atoi(digits[2:])
		}
		off := float64(zh*60+zm) * msPerMinute
		if zone[0] == '-' {
			off = -off
		}
		t -= off
	default:
		t = utcTime(t)
	}
	if math.Abs(t) > 8.64e15 {
		return 0, false
	}
	return t, true
}
