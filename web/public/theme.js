// Applied before the page paints, so a chosen theme never flashes the other one.
try {
  var t = localStorage.getItem("falcon.theme");
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
} catch (e) {}
