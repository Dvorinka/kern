// paint the right theme before first frame — paper or ink, never a flash
(function () {
	var t = localStorage.getItem("kern-theme");
	if (!t) t = matchMedia("(prefers-color-scheme: dark)").matches ? "ink" : "paper";
	document.documentElement.dataset.theme = t;
})();
