// Synthetic pathological player script for timeout/cache testing.
// Contains many function declarations to stress the meriyah-based
// preprocessing path in the pure-Go goja engine.
//
// Provenance: Generated deterministically to approximate the parse pressure
// of real YouTube player scripts (1-2 MB, thousands of function declarations).
// This fixture uses 200 padding functions (~8 KB) for CI-fast validation.
// The full representative workload (~500 KB, 2000 functions) is generated
// in-process by TestLargeGeneratedPlayerWorkload in solver_test.go.
// Neither fixture contains real player code, tokens, or challenge secrets.
(function () {
  var helper = { alr: function () {} };
  function Params(url, key, value) {
    this.values = { s: value, n: null };
  }
  Params.prototype.set = function (key, value) { this.values[key] = value; };
  Params.prototype.get = function (key) { return this.values[key]; };
  Params.prototype.clone = function () { return this; };
  Params.prototype.transform = function () {
    if (this.values.n) this.values.n = this.values.n.split("").reverse().join("") + "-n";
    if (this.values.s) this.values.s = this.values.s.split("").reverse().join("");
  };
  // Padding functions to increase parse time (simulates real player bloat).
  function pad0(a) { return a + 0; }
  function pad1(a) { return a + 1; }
  function pad2(a) { return a + 2; }
  function pad3(a) { return a + 3; }
  function pad4(a) { return a + 4; }
  function pad5(a) { return a + 5; }
  function pad6(a) { return a + 6; }
  function pad7(a) { return a + 7; }
  function pad8(a) { return a + 8; }
  function pad9(a) { return a + 9; }
  function pad10(a, b) { return a + 10 + b; }
  function pad11(a, b) { return a + 11 + b; }
  function pad12(a, b) { return a + 12 + b; }
  function pad13(a, b) { return a + 13 + b; }
  function pad14(a, b) { return a + 14 + b; }
  function pad15(a, b) { return a + 15 + b; }
  function pad16(a, b) { return a + 16 + b; }
  function pad17(a, b) { return a + 17 + b; }
  function pad18(a, b) { return a + 18 + b; }
  function pad19(a, b) { return a + 19 + b; }
  function pad20(a, b, c) { return a + 20 + b * 2 + c; }
  function pad21(a, b, c) { return a + 21 + b * 2 + c; }
  function pad22(a, b, c) { return a + 22 + b * 2 + c; }
  function pad23(a, b, c) { return a + 23 + b * 2 + c; }
  function pad24(a, b, c) { return a + 24 + b * 2 + c; }
  function pad25(a, b, c) { return a + 25 + b * 2 + c; }
  function pad26(a, b, c) { return a + 26 + b * 2 + c; }
  function pad27(a, b, c) { return a + 27 + b * 2 + c; }
  function pad28(a, b, c) { return a + 28 + b * 2 + c; }
  function pad29(a, b, c) { return a + 29 + b * 2 + c; }
  function pad30(a, b, c, d) { return a + 30 + b * 3 + c * 2 + d; }
  function pad31(a, b, c, d) { return a + 31 + b * 3 + c * 2 + d; }
  function pad32(a, b, c, d) { return a + 32 + b * 3 + c * 2 + d; }
  function pad33(a, b, c, d) { return a + 33 + b * 3 + c * 2 + d; }
  function pad34(a, b, c, d) { return a + 34 + b * 3 + c * 2 + d; }
  function pad35(a, b, c, d) { return a + 35 + b * 3 + c * 2 + d; }
  function pad36(a, b, c, d) { return a + 36 + b * 3 + c * 2 + d; }
  function pad37(a, b, c, d) { return a + 37 + b * 3 + c * 2 + d; }
  function pad38(a, b, c, d) { return a + 38 + b * 3 + c * 2 + d; }
  function pad39(a, b, c, d) { return a + 39 + b * 3 + c * 2 + d; }
  function pad40(a, b, c, d, e) { return a + 40 + b * 4 + c * 3 + d * 2 + e; }
  function pad41(a, b, c, d, e) { return a + 41 + b * 4 + c * 3 + d * 2 + e; }
  function pad42(a, b, c, d, e) { return a + 42 + b * 4 + c * 3 + d * 2 + e; }
  function pad43(a, b, c, d, e) { return a + 43 + b * 4 + c * 3 + d * 2 + e; }
  function pad44(a, b, c, d, e) { return a + 44 + b * 4 + c * 3 + d * 2 + e; }
  function pad45(a, b, c, d, e) { return a + 45 + b * 4 + c * 3 + d * 2 + e; }
  function pad46(a, b, c, d, e) { return a + 46 + b * 4 + c * 3 + d * 2 + e; }
  function pad47(a, b, c, d, e) { return a + 47 + b * 4 + c * 3 + d * 2 + e; }
  function pad48(a, b, c, d, e) { return a + 48 + b * 4 + c * 3 + d * 2 + e; }
  function pad49(a, b, c, d, e) { return a + 49 + b * 4 + c * 3 + d * 2 + e; }
  function candidate(url, key, value) {
    helper.alr("alr", "yes");
    return new Params(url, key, value);
  }
}).call(this);
