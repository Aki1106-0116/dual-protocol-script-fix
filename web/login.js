document.querySelector("#login").addEventListener("submit", async function (e) {
  e.preventDefault();
  var b = document.querySelector("#submit"),
    x = document.querySelector("#err");
  b.disabled = true;
  x.textContent = "";
  try {
    var r = await fetch("api/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password: document.querySelector("#pw").value }),
    });
    var d = await r.json();
    if (!r.ok) throw new Error(d.error || "登录失败");
    location.reload();
  } catch (err) {
    x.textContent = err.message;
  } finally {
    b.disabled = false;
  }
});
