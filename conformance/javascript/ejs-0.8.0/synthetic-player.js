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
  function candidate(url, key, value) {
    helper.alr("alr", "yes");
    return new Params(url, key, value);
  }
}).call(this);
