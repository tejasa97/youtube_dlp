// Synthetic pathological player script for timeout/cache testing.
// Contains many function declarations to stress the meriyah-based
// preprocessing path in the pure-Go goja engine.
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
  function candidate(url, key, value) {
    helper.alr("alr", "yes");
    return new Params(url, key, value);
  }
}).call(this);
