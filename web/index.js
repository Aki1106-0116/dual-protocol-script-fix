var $ = function (s) {
  return document.querySelector(s);
};
var state = { router: { routes: [] }, exits: [] },
  regions = [],
  nodeConfig = null,
  sourceNodeConfig = null,
  configAction = "save",
  nodeLinks = null;
var esc = function (v) {
  return String(v == null ? "" : v).replace(/[&<>"']/g, function (c) {
    return {
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      '"': "&quot;",
      "'": "&#39;",
    }[c];
  });
};
async function api(path, options) {
  var res = await fetch(String(path).replace(/^\//, ""), options || {}),
    data = {};
  try {
    data = await res.json();
  } catch (_e) {}
  if (res.status === 401) {
    location.reload();
    throw new Error("登录已失效");
  }
  if (!res.ok) throw new Error(data.error || "HTTP " + res.status);
  return data;
}
function toast(message, bad) {
  var el = $("#toast");
  el.textContent = message;
  el.className = "toast show" + (bad ? " bad" : "");
  clearTimeout(el.timer);
  el.timer = setTimeout(function () {
    el.className = "toast";
  }, 4000);
}
function setError(message) {
  var el = $("#error");
  el.textContent = message || "";
  el.className = "notice" + (message ? " show" : "");
}
function randomToken(length) {
  var chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789_-",
    bytes = new Uint8Array(length);
  crypto.getRandomValues(bytes);
  var out = "";
  for (var i = 0; i < length; i++) out += chars[bytes[i] % chars.length];
  return out;
}
function routeOptions(selected) {
  var html =
    '<option value="0"' +
    (selected === 0 ? " selected" : "") +
    ">VPS 直连出口</option>";
  state.exits.forEach(function (exit) {
    if (exit.status !== "up" && exit.slot !== selected) return;
    var label =
      "#" +
      exit.slot +
      " · " +
      (exit.node.country_code || "--") +
      " · " +
      (exit.exit_ip || exit.status);
    html +=
      '<option value="' +
      exit.slot +
      '"' +
      (exit.slot === selected ? " selected" : "") +
      ">" +
      esc(label) +
      "</option>";
  });
  return html;
}
function renderRoutes() {
  var routes = state.router.routes || [];
  $("#routing").innerHTML = routes
    .map(function (route) {
      var secondary =
          route.protocol === "hy2" ? "UDP 入站 · TCP/UDP 目标" : "Xray 主节点",
        icon = route.protocol === "hy2" ? "H2" : "VX",
        mode = route.mode === "direct" ? "VPS 直连" : "出口 #" + route.slot,
        note = route.note || (!route.ready ? "出口暂不可用" : "");
      return (
        '<article class="route-card"><div class="route-top"><div class="proto"><div class="proto-icon">' +
        icon +
        "</div><div><h3>" +
        esc(route.label) +
        "</h3><p>" +
        secondary +
        '</p></div></div><span class="mode">' +
        esc(mode) +
        '</span></div><div class="route-controls"><select data-route="' +
        route.protocol +
        '">' +
        routeOptions(route.slot) +
        '</select><button data-save="' +
        route.protocol +
        '">应用</button></div><div class="route-note' +
        (note ? " warn" : "") +
        '">' +
        esc(note) +
        "</div></article>"
      );
    })
    .join("");
}
function routeChips(slot) {
  return (state.router.routes || [])
    .filter(function (r) {
      return r.slot === slot;
    })
    .map(function (r) {
      return '<span class="chip">' + esc(r.label) + "</span>";
    })
    .join("");
}
function renderExits() {
  $("#count").textContent = state.exits.length
    ? state.exits.length + " 条"
    : "";
  if (!state.exits.length) {
    $("#exits").innerHTML =
      '<div class="empty"><b>还没有家宽出口</b>点击“新建出口”，按地区连接 VPN Gate 志愿节点。</div>';
    return;
  }
  $("#exits").innerHTML = state.exits
    .map(function (exit) {
      var node = exit.node || {},
        status = exit.status || "failed",
        statusText =
          {
            up: "已连通",
            starting: "连接中",
            failed: "失败",
            stopped: "已停止",
          }[status] || status;
      return (
        '<article class="exit"><div class="dot ' +
        esc(status) +
        '" title="' +
        esc(statusText) +
        '"></div><div class="ip"><strong>' +
        esc(exit.exit_ip || statusText) +
        "</strong><span>#" +
        exit.slot +
        " · " +
        esc(node.hostname || "") +
        '</span></div><div class="country">' +
        esc(node.country_code || "--") +
        ' <span style="color:var(--muted);font-size:10px">' +
        esc(node.country || "") +
        '</span></div><div class="meta"><b>127.0.0.1:' +
        exit.port +
        "</b><br><span>" +
        esc(exit.error || statusText) +
        '</span></div><div class="chips">' +
        routeChips(exit.slot) +
        '</div><div class="exit-actions"><button data-swap="' +
        exit.slot +
        '" ' +
        (status === "starting" ? "disabled" : "") +
        '>换 IP</button><button data-stop="' +
        exit.slot +
        '">停止</button></div></article>'
      );
    })
    .join("");
}
function renderJobs(jobs) {
  if (!jobs || !jobs.length) {
    $("#jobs").innerHTML = "";
    return;
  }
  $("#jobs").innerHTML =
    '<div class="section-head"><div><h2>后台任务</h2><p>失效节点会自动尝试同地区候选</p></div></div>' +
    jobs
      .map(function (job) {
        return (
          '<article class="job"><div class="job-head"><strong>' +
          esc(job.summary) +
          "</strong>" +
          (job.done
            ? '<button class="btn small" data-dismiss="' +
              job.id +
              '">关闭</button>'
            : '<span style="color:var(--amber)">进行中</span>') +
          '</div><div class="steps">' +
          job.steps
            .map(function (step) {
              return (
                '<span class="step ' +
                esc(step.status) +
                '" title="' +
                esc(step.detail || "") +
                '">' +
                esc(step.label) +
                " · " +
                esc(step.status) +
                "</span>"
              );
            })
            .join("") +
          "</div></article>"
        );
      })
      .join("");
}
async function load() {
  try {
    state = await api("api/status");
    setError(
      (state.storage_issues || [])
        .map(function (x) {
          return x.path + "：" + x.message;
        })
        .join("\n"),
    );
    $("#healthBadge").textContent = (state.storage_issues || []).length
      ? "存储异常 · 节点保持运行"
      : "管理服务在线";
    $("#quantity").max = state.max_exits || 12;
    var stack =
      state.router.stack === "xhttp_hy2"
        ? "VLESS XHTTP + Hysteria2"
        : state.router.stack === "reality_hy2"
          ? "VLESS REALITY Vision + Hysteria2"
          : "节点配置未识别";
    $("#stack").textContent = stack + " · 私有网络控制台";
    if (state.router.last_error)
      setError(
        state.router.last_error +
          "\n" +
          (state.storage_issues || [])
            .map(function (x) {
              return x.message;
            })
            .join("\n"),
      );
    renderRoutes();
    renderExits();
    renderJobs(await api("api/jobs"));
  } catch (err) {
    setError(err.message);
  }
}
async function loadRegions() {
  regions = await api("api/regions");
  $("#region").innerHTML =
    '<option value="">任意地区（速度优先）</option>' +
    regions
      .map(function (r) {
        return (
          '<option value="' +
          esc(r.code) +
          '">' +
          esc(r.code + " · " + r.name + " · " + r.available + " 个") +
          "</option>"
        );
      })
      .join("");
  updateRegionInfo();
}
function updateRegionInfo() {
  var code = $("#region").value,
    region = regions.find(function (r) {
      return r.code === code;
    });
  $("#regionInfo").textContent = region
    ? "可用 " +
      region.available +
      " · 最低延迟 " +
      region.best_ping +
      " ms · 标称 " +
      region.best_speed_mbps.toFixed(0) +
      " Mbps"
    : "从全部地区按标称速度选择";
}
function setValue(id, value) {
  var el = $(id);
  if (el) el.value = value == null ? "" : value;
}
function numberValue(id) {
  return Number($(id).value) || 0;
}
function updateConfigFields() {
  var xhttp = nodeConfig && nodeConfig.main_mode === "xhttp",
    reality = nodeConfig && nodeConfig.main_mode === "reality";
  $("#xhttpFields").classList.toggle("hidden", !xhttp);
  $("#realityFields").classList.toggle("hidden", !reality);
  var hopping = $("#cfgHY2PortMode").value === "hop";
  $("#singlePortField").classList.toggle("hidden", hopping);
  ["#firstPortField", "#endPortField", "#hopTypeField"].forEach(function (id) {
    $(id).classList.toggle("hidden", !hopping);
  });
  var random = hopping && $("#cfgHopType").value === "random";
  $("#fixedHopField").classList.toggle("hidden", !hopping || random);
  $("#minHopField").classList.toggle("hidden", !random);
  $("#maxHopField").classList.toggle("hidden", !random);
  var gecko = $("#cfgHY2ObfsType").value === "gecko";
  ["#obfsPasswordField", "#geckoMinField", "#geckoMaxField"].forEach(
    function (id) {
      $(id).classList.toggle("hidden", !gecko);
    },
  );
}
function populateNodeConfig(config) {
  nodeConfig = config;
  $("#configMode").textContent =
    (config.main_mode === "xhttp"
      ? "VLESS XHTTP + Hysteria2"
      : "VLESS REALITY Vision + Hysteria2") +
    (configAction === "reinstall" ? " · 完整重装" : " · 修改现有配置");
  setValue("#cfgNodeName", config.node_name);
  setValue("#cfgFP", config.fp);
  setValue("#cfgXHTTPDomain", config.xhttp_domain);
  setValue("#cfgXHTTPPath", config.xhttp_path);
  setValue("#cfgXHTTPUUID", config.xhttp_uuid);
  setValue("#cfgXHTTPBackendPort", config.xhttp_backend_port || 10080);
  setValue("#cfgRealityAddress", config.reality_address);
  setValue("#cfgRealitySNI", config.reality_sni);
  setValue("#cfgRealityUUID", config.reality_uuid);
  setValue("#cfgRealityPublicKey", config.reality_public_key);
  setValue("#cfgRealityShortID", config.reality_short_id);
  setValue("#cfgHY2ACMEEmail", config.hy2_acme_email);
  $("#cfgRotateReality").checked = !!config.rotate_reality_keys;
  setValue("#cfgHY2Domain", config.hy2_domain);
  setValue("#cfgHY2Password", config.hy2_password);
  setValue("#cfgHY2PortMode", config.hy2_port_mode);
  setValue("#cfgHY2Port", config.hy2_port);
  setValue("#cfgHY2FirstPort", config.hy2_first_port);
  setValue("#cfgHY2EndPort", config.hy2_end_port);
  setValue("#cfgHY2HopInterval", config.hy2_hop_interval || 25);
  setValue("#cfgHY2MinHopInterval", config.hy2_min_hop_interval || 15);
  setValue("#cfgHY2MaxHopInterval", config.hy2_max_hop_interval || 25);
  setValue(
    "#cfgHopType",
    config.hy2_min_hop_interval || config.hy2_max_hop_interval
      ? "random"
      : "fixed",
  );
  setValue("#cfgHY2ObfsType", config.hy2_obfs_type || "none");
  setValue("#cfgHY2ObfsPassword", config.hy2_obfs_password);
  setValue("#cfgGeckoMin", config.hy2_gecko_min_packet_size || 512);
  setValue("#cfgGeckoMax", config.hy2_gecko_max_packet_size || 1200);
  updateConfigFields();
}
function targetConfig(source, target) {
  var c = JSON.parse(JSON.stringify(source));
  c.main_mode = target;
  c.stack_mode = target === "xhttp" ? "xhttp_hy2" : "reality_hy2";
  c.hy2_cert_source = target === "xhttp" ? "caddy" : "acme";
  if (target === "xhttp") {
    if (source.main_mode !== "xhttp") {
      c.xhttp_domain = "";
      c.xhttp_path = "/" + randomToken(14);
      c.xhttp_uuid = crypto.randomUUID();
      c.xhttp_backend_port = 10080;
    }
    c.rotate_reality_keys = false;
  } else {
    if (source.main_mode !== "reality") {
      c.reality_address = c.hy2_domain;
      c.reality_sni = "www.microsoft.com";
      c.reality_uuid = crypto.randomUUID();
      c.reality_public_key = "";
      c.reality_short_id = "";
    }
    c.rotate_reality_keys = true;
  }
  return c;
}
function collectNodeConfig() {
  var c = Object.assign({}, nodeConfig);
  c.node_name = $("#cfgNodeName").value.trim();
  c.fp = $("#cfgFP").value;
  c.xhttp_domain = $("#cfgXHTTPDomain").value.trim();
  c.xhttp_path = $("#cfgXHTTPPath").value.trim();
  c.xhttp_uuid = $("#cfgXHTTPUUID").value.trim();
  c.xhttp_backend_port = numberValue("#cfgXHTTPBackendPort");
  c.reality_address = $("#cfgRealityAddress").value.trim();
  c.reality_sni = $("#cfgRealitySNI").value.trim();
  c.reality_uuid = $("#cfgRealityUUID").value.trim();
  c.rotate_reality_keys = $("#cfgRotateReality").checked;
  c.hy2_domain = $("#cfgHY2Domain").value.trim();
  c.hy2_password = $("#cfgHY2Password").value;
  c.hy2_port_mode = $("#cfgHY2PortMode").value;
  c.hy2_port = numberValue("#cfgHY2Port");
  c.hy2_first_port = numberValue("#cfgHY2FirstPort");
  c.hy2_end_port = numberValue("#cfgHY2EndPort");
  if (c.hy2_port_mode === "hop" && $("#cfgHopType").value === "random") {
    c.hy2_hop_interval = 0;
    c.hy2_min_hop_interval = numberValue("#cfgHY2MinHopInterval");
    c.hy2_max_hop_interval = numberValue("#cfgHY2MaxHopInterval");
  } else {
    c.hy2_hop_interval = numberValue("#cfgHY2HopInterval");
    c.hy2_min_hop_interval = 0;
    c.hy2_max_hop_interval = 0;
  }
  c.hy2_acme_email = $("#cfgHY2ACMEEmail").value.trim();
  c.hy2_obfs_type = $("#cfgHY2ObfsType").value;
  c.hy2_obfs_password = $("#cfgHY2ObfsPassword").value;
  c.hy2_gecko_min_packet_size = numberValue("#cfgGeckoMin");
  c.hy2_gecko_max_packet_size = numberValue("#cfgGeckoMax");
  return c;
}
function invalidField(id, message) {
  toast(message, true);
  var el = $(id);
  if (el) {
    el.focus();
    el.scrollIntoView({ behavior: "smooth", block: "center" });
  }
  return false;
}
function validUUID(value) {
  return /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/.test(
    value,
  );
}
function plausibleDomain(value) {
  return value.length <= 253 && value.indexOf(".") > 0 && !/[\s/:]/.test(value);
}
function validateNodeConfigForm(c) {
  if (!c.node_name || c.node_name.length > 80)
    return invalidField("#cfgNodeName", "请填写 1-80 个字符的节点名称");
  if (!plausibleDomain(c.hy2_domain))
    return invalidField("#cfgHY2Domain", "请填写有效的 HY2 域名");
  if (!c.hy2_password || c.hy2_password.length > 256)
    return invalidField("#cfgHY2Password", "请填写 HY2 密码（1-256 个字符）");
  var reserved = [80, 443, 2053, 8443];
  if (c.hy2_port_mode === "single") {
    if (c.hy2_port < 1 || c.hy2_port > 65535 || reserved.includes(c.hy2_port))
      return invalidField(
        "#cfgHY2Port",
        "HY2 单端口必须在 1-65535，且不能使用 80/443/2053/8443",
      );
  } else {
    if (
      c.hy2_first_port < 1 ||
      c.hy2_end_port > 65535 ||
      c.hy2_end_port <= c.hy2_first_port
    )
      return invalidField("#cfgHY2FirstPort", "请填写有效的 HY2 跳跃端口范围");
    if (
      reserved.some(function (p) {
        return p >= c.hy2_first_port && p <= c.hy2_end_port;
      })
    )
      return invalidField(
        "#cfgHY2FirstPort",
        "HY2 跳跃范围不能包含 80/443/2053/8443",
      );
    if ($("#cfgHopType").value === "random") {
      if (
        c.hy2_min_hop_interval < 5 ||
        c.hy2_max_hop_interval < c.hy2_min_hop_interval ||
        c.hy2_max_hop_interval > 600
      )
        return invalidField(
          "#cfgHY2MinHopInterval",
          "随机跳跃最小/最大间隔必须完整填写，范围为 5-600 秒",
        );
    } else if (c.hy2_hop_interval < 5 || c.hy2_hop_interval > 600)
      return invalidField("#cfgHY2HopInterval", "固定跳跃间隔必须在 5-600 秒");
  }
  if (c.hy2_obfs_type === "gecko") {
    if (!c.hy2_obfs_password)
      return invalidField(
        "#cfgHY2ObfsPassword",
        "启用 Gecko 时必须填写混淆密码",
      );
    if (
      c.hy2_gecko_min_packet_size < 64 ||
      c.hy2_gecko_max_packet_size < c.hy2_gecko_min_packet_size ||
      c.hy2_gecko_max_packet_size > 1500
    )
      return invalidField(
        "#cfgGeckoMin",
        "Gecko 分片必须在 64-1500，且最大值不能小于最小值",
      );
  }
  if (c.main_mode === "xhttp") {
    if (!plausibleDomain(c.xhttp_domain))
      return invalidField("#cfgXHTTPDomain", "请填写有效的 XHTTP 域名");
    if (c.xhttp_domain.toLowerCase() === c.hy2_domain.toLowerCase())
      return invalidField(
        "#cfgXHTTPDomain",
        "XHTTP 与 HY2 必须使用两个不同域名",
      );
    if (!c.xhttp_path || c.xhttp_path === "/" || c.xhttp_path.charAt(0) !== "/")
      return invalidField("#cfgXHTTPPath", "请填写以 / 开头的非根 XHTTP 路径");
    if (!validUUID(c.xhttp_uuid))
      return invalidField("#cfgXHTTPUUID", "XHTTP UUID 格式无效");
    if (c.xhttp_backend_port < 1024 || c.xhttp_backend_port > 65535)
      return invalidField(
        "#cfgXHTTPBackendPort",
        "XHTTP 后端端口必须在 1024-65535",
      );
  } else {
    if (!c.reality_address)
      return invalidField(
        "#cfgRealityAddress",
        "请填写 REALITY 客户端连接地址",
      );
    if (!plausibleDomain(c.reality_sni))
      return invalidField("#cfgRealitySNI", "请填写有效的 REALITY 目标 SNI");
    if (!validUUID(c.reality_uuid))
      return invalidField("#cfgRealityUUID", "REALITY UUID 格式无效");
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(c.hy2_acme_email))
      return invalidField(
        "#cfgHY2ACMEEmail",
        "请填写有效的 Hysteria2 ACME 邮箱",
      );
  }
  return true;
}
function changedNodeDomains(c) {
  var changes = [];
  if (!sourceNodeConfig) return changes;
  var oldHY2 = String(sourceNodeConfig.hy2_domain || "").toLowerCase(),
    newHY2 = String(c.hy2_domain || "").toLowerCase();
  if (oldHY2 !== newHY2)
    changes.push("HY2：" + (oldHY2 || "未设置") + " → " + newHY2);
  if (c.main_mode === "xhttp") {
    var oldX = String(sourceNodeConfig.xhttp_domain || "").toLowerCase(),
      newX = String(c.xhttp_domain || "").toLowerCase();
    if (oldX !== newX)
      changes.push("XHTTP：" + (oldX || "未设置") + " → " + newX);
  }
  return changes;
}
async function openNodeConfig(action) {
  configAction = action;
  sourceNodeConfig = await api("api/node-config");
  $("#targetWrap").classList.toggle("hidden", action !== "reinstall");
  $("#configTitle").textContent =
    action === "reinstall" ? "一键重装协议组合" : "节点配置";
  $("#saveConfig").textContent =
    action === "reinstall" ? "确认重装组合" : "保存并重启代理";
  $("#configWarning").className =
    "config-warning" + (action === "reinstall" ? " danger-warning" : "");
  $("#configWarning").textContent =
    action === "reinstall"
      ? "请完整填写所有带 * 的必填项后再开始。重装会短暂中断连接，并可能安装或移除 Caddy；域名必须正确解析到本 VPS、关闭 CDN 代理并放行 80/443。失败会恢复原配置。"
      : "保存会先校验全部必填项和候选配置，再重启并检查代理。若更改域名，会先取得并验证新证书，成功后才清理旧证书；失败会恢复旧配置。";
  if (action === "reinstall") {
    var target = sourceNodeConfig.main_mode === "xhttp" ? "reality" : "xhttp";
    setValue("#cfgTarget", target);
    populateNodeConfig(targetConfig(sourceNodeConfig, target));
  } else populateNodeConfig(sourceNodeConfig);
  $("#configModal").classList.add("open");
}
async function copyText(id) {
  var text = $("#" + id).value;
  try {
    await navigator.clipboard.writeText(text);
  } catch (_e) {
    var el = $("#" + id);
    el.select();
    document.execCommand("copy");
  }
  toast("已复制");
}

document.addEventListener("click", async function (event) {
  var close = event.target.closest("[data-close]");
  if (close) {
    $("#" + close.dataset.close).classList.remove("open");
    return;
  }
  var save = event.target.closest("[data-save]");
  if (save) {
    var protocol = save.dataset.save,
      select = document.querySelector('[data-route="' + protocol + '"]');
    save.disabled = true;
    try {
      await api(
        "api/bind?protocol=" +
          encodeURIComponent(protocol) +
          "&slot=" +
          select.value,
        { method: "POST" },
      );
      toast("协议出口已更新");
      await load();
    } catch (err) {
      toast(err.message, true);
    } finally {
      save.disabled = false;
    }
    return;
  }
  var swap = event.target.closest("[data-swap]");
  if (swap) {
    swap.disabled = true;
    try {
      await api("api/swap?slot=" + swap.dataset.swap, { method: "POST" });
      toast("正在切换家宽 IP");
      await load();
    } catch (err) {
      toast(err.message, true);
    } finally {
      swap.disabled = false;
    }
    return;
  }
  var stop = event.target.closest("[data-stop]");
  if (stop) {
    if (
      !confirm("停止出口 #" + stop.dataset.stop + "？绑定协议会切回 VPS 直连。")
    )
      return;
    try {
      await api("api/stop?slot=" + stop.dataset.stop, { method: "POST" });
      await load();
    } catch (err) {
      toast(err.message, true);
    }
    return;
  }
  var dismiss = event.target.closest("[data-dismiss]");
  if (dismiss) {
    await api("api/jobs/dismiss?id=" + dismiss.dataset.dismiss, {
      method: "POST",
    });
    await load();
    return;
  }
  var generate = event.target.closest("[data-generate]");
  if (generate) {
    if (generate.dataset.generate === "path")
      setValue("#cfgXHTTPPath", "/" + randomToken(14));
    else if (generate.dataset.generate === "xuuid")
      setValue("#cfgXHTTPUUID", crypto.randomUUID());
    else if (generate.dataset.generate === "ruuid")
      setValue("#cfgRealityUUID", crypto.randomUUID());
    else if (generate.dataset.generate === "hy2pass")
      setValue("#cfgHY2Password", randomToken(32));
    else if (generate.dataset.generate === "geckopass")
      setValue("#cfgHY2ObfsPassword", randomToken(32));
    return;
  }
  var copy = event.target.closest("[data-copy]");
  if (copy) {
    await copyText(copy.dataset.copy);
  }
});
document.addEventListener("click", async function (event) {
  var b = event.target.closest("[data-restart-service]");
  if (!b) return;
  var service = b.dataset.restartService,
    label = service === "all" ? "当前组合全部服务" : service;
  if (!confirm("确认重启 " + label + "？当前连接会短暂中断。")) return;
  b.disabled = true;
  var old = b.textContent;
  b.textContent = "重启并检查中…";
  try {
    var result = await api("api/services/restart", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ service: service }),
    });
    toast("重启成功：" + Object.keys(result.results).join("、"));
  } catch (err) {
    toast(err.message, true);
  } finally {
    b.disabled = false;
    b.textContent = old;
  }
});
$("#add").onclick = async function () {
  document.querySelector('[data-view="exitsView"]').click();
  try {
    await loadRegions();
    $("#exitModal").classList.add("open");
  } catch (err) {
    toast(err.message, true);
  }
};
$("#region").onchange = updateRegionInfo;
$("#create").onclick = async function () {
  var b = this,
    count = Math.max(
      1,
      Math.min(state.max_exits || 12, Number($("#quantity").value) || 1),
    );
  b.disabled = true;
  try {
    await api(
      "api/provision?region=" +
        encodeURIComponent($("#region").value) +
        "&count=" +
        count,
      { method: "POST" },
    );
    $("#exitModal").classList.remove("open");
    toast("创建任务已提交");
    await load();
  } catch (err) {
    toast(err.message, true);
  } finally {
    b.disabled = false;
  }
};
$("#refresh").onclick = async function () {
  var b = this;
  b.disabled = true;
  try {
    var result = await api("api/refresh", { method: "POST" });
    toast("已刷新 " + result.count + " 个节点");
    await load();
  } catch (err) {
    toast(err.message, true);
  } finally {
    b.disabled = false;
  }
};
$("#serviceControl").onclick = function () {
  var stack = state.router.stack;
  if (stack !== "xhttp_hy2" && stack !== "reality_hy2") {
    toast("当前节点配置未识别，不能安全重启组合服务", true);
    return;
  }
  var xhttp = stack === "xhttp_hy2";
  $("#serviceStack").textContent = xhttp
    ? "当前组合：VLESS XHTTP + Hysteria2（含 Caddy）"
    : "当前组合：VLESS REALITY Vision + Hysteria2（不安装 Caddy）";
  $("#restartCaddy").classList.toggle("hidden", !xhttp);
  $("#serviceModal").classList.add("open");
};
$("#configure").onclick = async function () {
  var b = this;
  b.disabled = true;
  try {
    await openNodeConfig("save");
  } catch (err) {
    toast(err.message, true);
  } finally {
    b.disabled = false;
  }
};
$("#reinstall").onclick = async function () {
  var b = this;
  b.disabled = true;
  try {
    await openNodeConfig("reinstall");
  } catch (err) {
    toast(err.message, true);
  } finally {
    b.disabled = false;
  }
};
$("#cfgTarget").onchange = function () {
  populateNodeConfig(targetConfig(sourceNodeConfig, this.value));
};
$("#cfgHY2PortMode").onchange = updateConfigFields;
$("#cfgHopType").onchange = updateConfigFields;
$("#cfgHY2ObfsType").onchange = updateConfigFields;
$("#saveConfig").onclick = async function () {
  if (!nodeConfig) return;
  var reinstall = configAction === "reinstall",
    payload = collectNodeConfig();
  if (!validateNodeConfigForm(payload)) return;
  var changes = changedNodeDomains(payload),
    question = reinstall
      ? "确认完整重装所选协议组合？节点会暂时断开，成功后面板也会重启。"
      : "保存后将立即重启代理服务；若健康检查失败会自动回滚。继续吗？";
  if (changes.length)
    question =
      "检测到域名变更：\n" +
      changes.join("\n") +
      "\n\n请再次确认：新域名已正确解析到本 VPS，Cloudflare/CDN 代理已关闭，TCP 80/443 与 HY2 UDP 端口已放行。系统会先签发并验证新证书，成功后再清理旧证书；失败则自动恢复。\n\n继续吗？";
  if (!confirm(question)) return;
  var b = this;
  b.disabled = true;
  b.textContent = reinstall ? "重装中，请勿关闭…" : "校验、换证并重启中…";
  try {
    var result = await api(
      reinstall ? "api/node-reinstall" : "api/node-config",
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      },
    );
    $("#configModal").classList.remove("open");
    if (result.warning) toast(result.warning, true);
    else toast(reinstall ? "协议组合重装完成" : "节点配置已保存");
    if (result.panel_restart)
      alert(
        "HY2 域名和面板证书已更新，面板将重启。请稍后使用域名 " +
          result.panel_domain +
          "、原面板端口和路径重新打开。",
      );
    else await load();
  } catch (err) {
    toast(err.message, true);
  } finally {
    b.disabled = false;
    b.textContent = reinstall ? "确认重装组合" : "保存并重启代理";
  }
};
$("#share").onclick = async function () {
  var b = this;
  b.disabled = true;
  try {
    nodeLinks = await api("api/node-links");
    setValue("#mainURL", nodeLinks.main_url);
    setValue("#hy2URL", nodeLinks.hy2_url);
    setValue("#nodeInfo", nodeLinks.info);
    $("#shareModal").classList.add("open");
  } catch (err) {
    toast(err.message, true);
  } finally {
    b.disabled = false;
  }
};
$("#downloadLinks").onclick = function () {
  if (!nodeLinks) return;
  var blob = new Blob(
      [
        nodeLinks.info +
          "\n\n主节点：\n" +
          nodeLinks.main_url +
          "\n\nHysteria2：\n" +
          nodeLinks.hy2_url +
          "\n",
      ],
      { type: "text/plain;charset=utf-8" },
    ),
    url = URL.createObjectURL(blob),
    a = document.createElement("a");
  a.href = url;
  a.download = "dual-protocol-nodes.txt";
  a.click();
  setTimeout(function () {
    URL.revokeObjectURL(url);
  }, 1000);
};
$("#panelSettings").onclick = async function () {
  var b = this;
  b.disabled = true;
  try {
    var c = await api("api/panel-config");
    setValue("#panelDomain", c.domain);
    setValue("#panelPort", c.port);
    setValue("#panelPath", c.path);
    setValue("#panelMax", c.max_exits);
    setValue("#panelPassword", "");
    $("#panelModal").classList.add("open");
  } catch (err) {
    toast(err.message, true);
  } finally {
    b.disabled = false;
  }
};
$("#randomPanelPath").onclick = function () {
  setValue("#panelPath", randomToken(24));
};
$("#randomPanelPassword").onclick = function () {
  setValue("#panelPassword", randomToken(28));
};
$("#savePanel").onclick = async function () {
  if (!confirm("保存后面板将重启，当前页面会断开。继续吗？")) return;
  var b = this,
    payload = {
      domain: $("#panelDomain").value,
      port: numberValue("#panelPort"),
      path: $("#panelPath").value.trim(),
      max_exits: numberValue("#panelMax"),
      new_password: $("#panelPassword").value,
      password_configured: true,
    };
  b.disabled = true;
  b.textContent = "保存中…";
  try {
    var r = await api("api/panel-config", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    $("#panelModal").classList.remove("open");
    alert(
      "面板设置已保存并将重启。新地址：\nhttps://" +
        r.domain +
        ":" +
        r.port +
        "/" +
        r.path +
        "/\n\n若修改了密码，请使用新密码登录。",
    );
  } catch (err) {
    toast(err.message, true);
  } finally {
    b.disabled = false;
    b.textContent = "保存并重启面板";
  }
};
load();
var loading = false;
async function poll() {
  if (!document.hidden && !loading) {
    loading = true;
    try {
      await load();
    } finally {
      loading = false;
    }
  }
  setTimeout(
    poll,
    (state.exits || []).some(function (t) {
      return t.status === "starting";
    })
      ? 3000
      : 10000,
  );
}
setTimeout(poll, 10000);
document.addEventListener("visibilitychange", function () {
  if (!document.hidden) load();
});
document.addEventListener("click", function (e) {
  var n = e.target.closest("[data-view]");
  if (n) {
    document.querySelectorAll(".view").forEach(function (v) {
      v.classList.toggle("hidden", v.id !== n.dataset.view);
    });
    document.querySelectorAll("[data-view]").forEach(function (b) {
      b.classList.toggle("active", b === n);
    });
  }
  var o = e.target.closest("[data-open]");
  if (o) document.getElementById(o.dataset.open).click();
});
document.addEventListener("click", function (e) {
  var b = e.target.closest("[data-download]");
  if (!b) return;
  var key = b.dataset.download,
    text = nodeLinks && nodeLinks[key];
  if (!text) {
    toast("当前客户端配置尚未生成，请在节点配置中保存后重试", true);
    return;
  }
  var url = URL.createObjectURL(
      new Blob([text], {
        type: key.endsWith("yaml") ? "text/yaml" : "application/json",
      }),
    ),
    a = document.createElement("a");
  a.href = url;
  a.download = key.replace("_client_", "-client.");
  a.click();
  setTimeout(function () {
    URL.revokeObjectURL(url);
  }, 1000);
});
document.querySelectorAll(".modal").forEach(function (m) {
  m.setAttribute("role", "dialog");
  m.setAttribute("aria-modal", "true");
  m.addEventListener("click", function (e) {
    if (e.target === m) m.classList.remove("open");
  });
});
document.querySelectorAll(".close").forEach(function (b) {
  b.setAttribute("aria-label", "关闭");
});
document.addEventListener("keydown", function (e) {
  if (e.key === "Escape")
    document.querySelectorAll(".modal.open").forEach(function (m) {
      m.classList.remove("open");
    });
});

let dialogFocus = null;
document.querySelectorAll(".modal").forEach(function (modal) {
  new MutationObserver(function () {
    if (modal.classList.contains("open")) {
      dialogFocus = document.activeElement;
      document.body.style.overflow = "hidden";
      const first = modal.querySelector(
        "input:not([readonly]), select, button",
      );
      if (first) first.focus();
    } else if (!document.querySelector(".modal.open")) {
      document.body.style.overflow = "";
      if (dialogFocus) dialogFocus.focus();
    }
  }).observe(modal, { attributes: true, attributeFilter: ["class"] });
});
document.addEventListener("keydown", function (event) {
  if (event.key !== "Tab") return;
  const modal = document.querySelector(".modal.open");
  if (!modal) return;
  const focusable = Array.from(
    modal.querySelectorAll(
      "button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled])",
    ),
  ).filter((el) => el.offsetParent !== null);
  const first = focusable[0],
    last = focusable[focusable.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    last.focus();
    event.preventDefault();
  } else if (!event.shiftKey && document.activeElement === last) {
    first.focus();
    event.preventDefault();
  }
});
