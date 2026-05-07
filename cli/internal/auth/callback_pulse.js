(function () {
  if (window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
    return;
  }
  var canvas = document.getElementById("pulse");
  if (!canvas) return;
  var ctx = canvas.getContext("2d");
  if (!ctx) return;

  var PURPLE = [145, 97, 242];
  var ORANGE = [255, 191, 110];
  var GRID = 24;
  var SPAWN_INTERVAL = 900;

  var pulses = [];
  var lastSpawn = 0;
  var animFrame = 0;

  function resize() {
    var dpr = window.devicePixelRatio || 1;
    canvas.width = canvas.offsetWidth * dpr;
    canvas.height = canvas.offsetHeight * dpr;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    pulses.length = 0;
  }

  function spawn() {
    var w = canvas.offsetWidth;
    var h = canvas.offsetHeight;
    var horizontal = Math.random() > 0.5;
    var color = Math.random() > 0.5 ? PURPLE : ORANGE;
    var opacity = 0.25 + Math.random() * 0.35;
    var length = 80 + Math.random() * 160;
    if (horizontal) {
      var row = Math.floor(Math.random() * (h / GRID)) * GRID;
      pulses.push({
        x: -60,
        y: row,
        horizontal: true,
        speed: 0.8 + Math.random() * 1.2,
        length: length,
        color: color,
        opacity: opacity,
      });
    } else {
      var col = Math.floor(Math.random() * (w / GRID)) * GRID;
      pulses.push({
        x: col,
        y: -60,
        horizontal: false,
        speed: 0.6 + Math.random() * 1.0,
        length: length,
        color: color,
        opacity: opacity,
      });
    }
  }

  function drawPulse(p) {
    var r = p.color[0], g = p.color[1], b = p.color[2];
    if (p.horizontal) {
      var headX = p.x;
      var tailX = p.x - p.length;
      var grad = ctx.createLinearGradient(tailX, p.y, headX, p.y);
      grad.addColorStop(0, "rgba(" + r + "," + g + "," + b + ",0)");
      grad.addColorStop(0.3, "rgba(" + r + "," + g + "," + b + "," + p.opacity * 0.5 + ")");
      grad.addColorStop(0.7, "rgba(" + r + "," + g + "," + b + "," + p.opacity + ")");
      grad.addColorStop(1, "rgba(" + r + "," + g + "," + b + ",0)");
      ctx.beginPath();
      ctx.moveTo(tailX, p.y);
      ctx.lineTo(headX, p.y);
      ctx.strokeStyle = grad;
      ctx.lineWidth = 1;
      ctx.stroke();

      ctx.beginPath();
      ctx.arc(headX, p.y, 2, 0, Math.PI * 2);
      ctx.fillStyle = "rgba(" + r + "," + g + "," + b + "," + p.opacity * 0.85 + ")";
      ctx.fill();

      var gg = ctx.createRadialGradient(headX, p.y, 0, headX, p.y, 14);
      gg.addColorStop(0, "rgba(" + r + "," + g + "," + b + "," + p.opacity * 0.35 + ")");
      gg.addColorStop(1, "rgba(" + r + "," + g + "," + b + ",0)");
      ctx.beginPath();
      ctx.arc(headX, p.y, 14, 0, Math.PI * 2);
      ctx.fillStyle = gg;
      ctx.fill();
    } else {
      var headY = p.y;
      var tailY = p.y - p.length;
      var grad2 = ctx.createLinearGradient(p.x, tailY, p.x, headY);
      grad2.addColorStop(0, "rgba(" + r + "," + g + "," + b + ",0)");
      grad2.addColorStop(0.3, "rgba(" + r + "," + g + "," + b + "," + p.opacity * 0.5 + ")");
      grad2.addColorStop(0.7, "rgba(" + r + "," + g + "," + b + "," + p.opacity + ")");
      grad2.addColorStop(1, "rgba(" + r + "," + g + "," + b + ",0)");
      ctx.beginPath();
      ctx.moveTo(p.x, tailY);
      ctx.lineTo(p.x, headY);
      ctx.strokeStyle = grad2;
      ctx.lineWidth = 1;
      ctx.stroke();

      ctx.beginPath();
      ctx.arc(p.x, headY, 2, 0, Math.PI * 2);
      ctx.fillStyle = "rgba(" + r + "," + g + "," + b + "," + p.opacity * 0.85 + ")";
      ctx.fill();

      var gg2 = ctx.createRadialGradient(p.x, headY, 0, p.x, headY, 14);
      gg2.addColorStop(0, "rgba(" + r + "," + g + "," + b + "," + p.opacity * 0.35 + ")");
      gg2.addColorStop(1, "rgba(" + r + "," + g + "," + b + ",0)");
      ctx.beginPath();
      ctx.arc(p.x, headY, 14, 0, Math.PI * 2);
      ctx.fillStyle = gg2;
      ctx.fill();
    }
  }

  function frame() {
    var w = canvas.offsetWidth;
    var h = canvas.offsetHeight;
    ctx.clearRect(0, 0, w, h);

    var now = Date.now();
    if (now - lastSpawn > SPAWN_INTERVAL * (0.5 + Math.random())) {
      spawn();
      lastSpawn = now;
    }

    pulses = pulses.filter(function (p) {
      if (p.horizontal) return p.x - p.length < w;
      return p.y - p.length < h;
    });

    for (var i = 0; i < pulses.length; i++) {
      var p = pulses[i];
      if (p.horizontal) p.x += p.speed;
      else p.y += p.speed;
      drawPulse(p);
    }

    animFrame = requestAnimationFrame(frame);
  }

  function stop() {
    if (animFrame) {
      cancelAnimationFrame(animFrame);
      animFrame = 0;
    }
  }

  function start() {
    if (!animFrame) {
      frame();
    }
  }

  resize();
  window.addEventListener("resize", resize);
  document.addEventListener("visibilitychange", function () {
    if (document.hidden) stop();
    else start();
  });
  window.addEventListener("pagehide", stop);
  frame();
})();
