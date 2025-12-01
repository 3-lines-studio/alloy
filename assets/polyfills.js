var globalThis = this;
var window = this;
var self = this;
var process = { env: { NODE_ENV: 'production' } };
var console = console || { log: function(){}, warn: function(){}, error: function(){}, info: function(){}, debug: function(){} };
var performance = performance || { now: function() { return Date.now(); } };

function TextEncoder() {}
TextEncoder.prototype.encode = function(str) {
	var arr = [];
	for (var i = 0; i < str.length; i++) {
		var c = str.charCodeAt(i);
		if (c < 128) arr.push(c);
		else if (c < 2048) { arr.push(192 | (c >> 6)); arr.push(128 | (c & 63)); }
		else { arr.push(224 | (c >> 12)); arr.push(128 | ((c >> 6) & 63)); arr.push(128 | (c & 63)); }
	}
	return new Uint8Array(arr);
};

function TextDecoder() {}
TextDecoder.prototype.decode = function(arr) {
	var str = '';
	for (var i = 0; i < arr.length; i++) str += String.fromCharCode(arr[i]);
	return str;
};
