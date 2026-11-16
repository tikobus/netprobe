/* NetProbe Web UI — 零依赖单页应用 */
"use strict";

const $ = (sel, el = document) => el.querySelector(sel);
const app = $("#app");

/* ---------- 基础工具 ---------- */

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, c =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function fmtTime(ts) {
  if (!ts) return "-";
  const d = new Date(ts);
  const p = n => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function fmtMs(v) {
  if (v == null) return "-";
  if (v >= 1000) return (v / 1000).toFixed(2) + " s";
  if (v >= 100) return Math.round(v) + " ms";
  return v.toFixed(2) + " ms";
}

function toast(msg, isErr = false) {
  const el = $("#toast");
  el.textContent = msg;
  el.style.borderColor = isErr ? "var(--bad)" : "var(--border)";
  el.classList.remove("hidden");
  clearTimeout(el._t);
  el._t = setTimeout(() => el.classList.add("hidden"), 2600);
}

async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  if (res.status === 204) return null;
  let body = null;
  try { body = await res.json(); } catch (_) { /* ignore */ }
  if (!res.ok) {
    const msg = (body && body.error) || `HTTP ${res.status}`;
    throw new Error(msg);
  }
  return body;
}

/* ---------- SVG 折线图（延迟趋势，红点标记失败） ---------- */

function lineChart(points, opts = {}) {
  const W = 900, H = 180, padL = 52, padR = 14, padT = 14, padB = 24;
  if (!points.length) return `<div class="loading">暂无数据</div>`;

  const maxV = Math.max(...points.map(p => p.v), 1) * 1.15;
  const iw = W - padL - padR, ih = H - padT - padB;
  const x = i => padL + (points.length === 1 ? iw / 2 : (i / (points.length - 1)) * iw);
  const y = v => padT + ih - (v / maxV) * ih;

  let path = "", area = "", dots = "";
  points.forEach((p, i) => {
    const cmd = i === 0 ? "M" : "L";
    path += `${cmd}${x(i).toFixed(1)},${y(p.v).toFixed(1)}`;
    area = path;
  });
  area += `L${x(points.length - 1).toFixed(1)},${(padT + ih).toFixed(1)}L${padL},${(padT + ih).toFixed(1)}Z`;

  const fails = points.map((p, i) => p.ok ? "" :
    `<circle cx="${x(i).toFixed(1)}" cy="${y(p.v).toFixed(1)}" r="4" fill="var(--bad)"/>`).join("");

  // y 轴刻度（4 档）
  const ticks = [0, 1, 2, 3].map(k => {
    const v = maxV * k / 3;
    return `<line x1="${padL}" x2="${W - padR}" y1="${y(v)}" y2="${y(v)}" stroke="var(--border)" stroke-dasharray="3,4"/>
            <text x="${padL - 6}" y="${y(v) + 4}" text-anchor="end" class="axis" fill="var(--muted)" font-size="11">${fmtMs(v)}</text>`;
  }).join("");

  return `
  <div class="chart-box"><svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">
    ${ticks}
    <path d="${area}" fill="rgba(77,163,255,.12)"/>
    <path d="${path}" fill="none" stroke="var(--accent)" stroke-width="1.8"/>
    ${fails}
    <text x="${padL}" y="${H - 6}" class="axis" fill="var(--muted)" font-size="11">${fmtTime(points[0].t)}</text>
    <text x="${W - padR}" y="${H - 6}" text-anchor="end" fill="var(--muted)" font-size="11">${fmtTime(points[points.length - 1].t)}</text>
  </svg></div>`;
}

/* ---------- 路由 ---------- */

const routes = {
  "": renderDash,
  "logs": renderLogs,
  "device": renderDeviceDetail,
};

window.addEventListener("hashchange", route);
window.addEventListener("DOMContentLoaded", route);

function route() {
  const hash = location.hash.replace(/^#\/?/, "");
  const [name, arg] = hash.split("/");
  (routes[name] || renderDash)(arg);
  $("#nav-dash").classList.toggle("active", name === "" );
  $("#nav-logs").classList.toggle("active", name === "logs");
}

/* ---------- 总览 ---------- */

async function renderDash() {
  app.innerHTML = `<div class="loading">加载中…</div>`;
  let ov;
  try {
    ov = await api("/api/report/overview?hours=24");
  } catch (e) {
    app.innerHTML = `<div class="loading">加载失败：${esc(e.message)}</div>`;
    return;
  }

  const withData = ov.items.filter(i => i.avail_pct >= 0);
  const avgAvail = withData.length
    ? (withData.reduce((s, i) => s + i.avail_pct, 0) / withData.length) : -1;

  app.innerHTML = `
  <div class="cards">
    <div class="card"><div class="num">${ov.total}</div><div class="label">设备总数</div></div>
    <div class="card ok"><div class="num">${ov.up}</div><div class="label">在线</div></div>
    <div class="card bad"><div class="num">${ov.down}</div><div class="label">离线</div></div>
    <div class="card"><div class="num">${avgAvail < 0 ? "-" : avgAvail.toFixed(1) + "%"}</div><div class="label">24h 平均可用率</div></div>
  </div>
  <div class="panel">
    <h3>设备列表（${ov.total}）</h3>
    <div style="overflow-x:auto">
    <table>
      <thead><tr>
        <th>状态</th><th>名称</th><th>类型</th><th>探活方式</th><th>目标</th>
        <th>周期</th><th>延迟</th><th>24h 可用率</th><th>24h 成功/总次</th><th style="width:190px">操作</th>
      </tr></thead>
      <tbody>
      ${ov.items.map(i => {
        const st = !i.enabled
          ? `<span class="badge off">停用</span>`
          : i.up == null ? `<span class="badge off">无数据</span>`
          : i.up ? `<span class="badge up">在线</span>` : `<span class="badge down">离线</span>`;
        const avail = i.avail_pct < 0 ? "-" :
          `<span style="color:${i.avail_pct >= 99 ? "var(--ok)" : i.avail_pct >= 90 ? "var(--warn)" : "var(--bad)"}">${i.avail_pct.toFixed(1)}%</span>`;
        return `<tr>
          <td>${st}</td>
          <td><a class="btn link" href="#/device/${i.id}">${esc(i.name)}</a></td>
          <td>${esc(i.type)}</td>
          <td>${esc(i.method)}</td>
          <td class="mono wrap">${esc(i.target)}</td>
          <td>${i.interval}s</td>
          <td>${fmtMs(i.latency_ms)}</td>
          <td>${avail}</td>
          <td class="muted">${i.success}/${i.total}</td>
          <td>
            <button class="btn link" onclick="ui.testDevice(${i.id}, this)">测试</button>
            <button class="btn link" onclick="ui.openDeviceForm(${i.id})">编辑</button>
            <button class="btn link danger" onclick="ui.deleteDevice(${i.id}, '${esc(i.name)}')">删除</button>
          </td>
        </tr>`;
      }).join("")}
      </tbody>
    </table>
    </div>
  </div>`;
}

/* ---------- 设备详情 ---------- */

async function renderDeviceDetail(id) {
  app.innerHTML = `<div class="loading">加载中…</div>`;
  let dev, logs;
  try {
    [dev, logs] = await Promise.all([
      api(`/api/devices/${id}`),
      api(`/api/logs?device_id=${id}&limit=300`),
    ]);
  } catch (e) {
    app.innerHTML = `<div class="loading">加载失败：${esc(e.message)}</div>`;
    return;
  }

  const items = (logs.items || []).slice().reverse(); // 时间升序
  const points = items.map(l => ({ t: l.probed_at, v: l.latency_ms, ok: l.success }));
  const total = items.length, ok = items.filter(l => l.success).length;

  app.innerHTML = `
  <a class="back-link" href="#/">← 返回总览</a>
  <div class="cards">
    <div class="card"><div class="num">${esc(dev.name)}</div><div class="label">${esc(dev.type)} / ${esc(dev.method)}</div></div>
    <div class="card ${ok === total && total ? "ok" : ok === 0 ? "bad" : ""}"><div class="num">${total ? (ok / total * 100).toFixed(1) + "%" : "-"}</div><div class="label">近期成功率（${total} 次）</div></div>
    <div class="card"><div class="num">${fmtMs(items.filter(l => l.success).map(l => l.latency_ms).reduce((a, b) => a + b, 0) / (ok || 1))}</div><div class="label">平均延迟</div></div>
  </div>
  <div class="panel">
    <h3>延迟趋势（最近 ${total} 次探测，红点为失败）</h3>
    ${lineChart(points)}
  </div>
  <div class="panel">
    <h3>设备信息</h3>
    <div class="kv">
      <span class="k">目标</span><span class="mono">${esc(dev.target)}</span>
      <span class="k">探测周期</span><span>${dev.interval}s（超时 ${dev.timeout}s，重试 ${dev.retries}）</span>
      <span class="k">期望条件</span><span class="mono">${esc(dev.expect || "默认")}</span>
      <span class="k">标签</span><span>${esc(dev.tags || "-")}</span>
      <span class="k">状态</span><span>${dev.enabled ? "启用" : "停用"}</span>
      <span class="k">创建时间</span><span>${fmtTime(dev.created_at)}</span>
    </div>
  </div>
  <div class="panel">
    <h3>探测记录（最近 ${Math.min(total, 100)} 条）</h3>
    <div style="overflow-x:auto; max-height:420px; overflow-y:auto">
    <table>
      <thead><tr><th>时间</th><th>结果</th><th>延迟</th><th>详情</th></tr></thead>
      <tbody>
      ${items.slice(-100).reverse().map(l => `
        <tr>
          <td class="mono">${fmtTime(l.probed_at)}</td>
          <td><span class="badge ${l.success ? "ok" : "fail"}">${l.success ? "成功" : "失败"}</span></td>
          <td>${fmtMs(l.latency_ms)}</td>
          <td class="wrap mono">${esc(l.detail)}</td>
        </tr>`).join("")}
      </tbody>
    </table>
    </div>
  </div>`;
}

/* ---------- 探测日志 ---------- */

const logsState = { device_id: "", result: "", offset: 0 };

async function renderLogs() {
  app.innerHTML = `
  <div class="panel">
    <h3>探测日志</h3>
    <div class="filters">
      <select id="f-device"><option value="">全部设备</option></select>
      <select id="f-result">
        <option value="">全部结果</option>
        <option value="success">仅成功</option>
        <option value="failure">仅失败</option>
      </select>
      <button class="btn" onclick="ui.queryLogs(0)">查询</button>
      <span class="muted" id="logs-hint"></span>
    </div>
    <div style="overflow-x:auto">
    <table>
      <thead><tr><th>ID</th><th>时间</th><th>设备</th><th>结果</th><th>延迟</th><th>详情</th></tr></thead>
      <tbody id="logs-body"><tr><td colspan="6" class="loading">加载中…</td></tr></tbody>
    </table>
    </div>
    <div style="margin-top:14px; display:flex; gap:10px">
      <button class="btn" onclick="ui.queryLogs(-100)">上一页</button>
      <button class="btn" onclick="ui.queryLogs(100)">下一页</button>
    </div>
  </div>`;

  // 设备下拉
  try {
    const devices = await api("/api/devices");
    const sel = $("#f-device");
    (devices || []).forEach(d => {
      const o = document.createElement("option");
      o.value = d.id; o.textContent = `${d.name} (#${d.id})`;
      sel.appendChild(o);
    });
    sel.value = logsState.device_id;
    $("#f-result").value = logsState.result;
  } catch (_) { /* ignore */ }
  queryLogs(0);
}

async function queryLogs(offsetDelta) {
  logsState.device_id = $("#f-device").value;
  logsState.result = $("#f-result").value;
  logsState.offset = Math.max(0, logsState.offset + offsetDelta);

  const p = new URLSearchParams({ limit: 100, offset: logsState.offset });
  if (logsState.device_id) p.set("device_id", logsState.device_id);
  if (logsState.result) p.set("result", logsState.result);

  const body = $("#logs-body");
  body.innerHTML = `<tr><td colspan="6" class="loading">加载中…</td></tr>`;
  let data;
  try {
    data = await api(`/api/logs?${p}`);
  } catch (e) {
    body.innerHTML = `<tr><td colspan="6" class="loading">加载失败：${esc(e.message)}</td></tr>`;
    return;
  }
  const names = {};
  try { (await api("/api/devices") || []).forEach(d => names[d.id] = d.name); } catch (_) {}

  $("#logs-hint").textContent = `offset = ${logsState.offset}，本页 ${(data.items || []).length} 条`;
  body.innerHTML = (data.items || []).map(l => `
    <tr>
      <td class="muted">${l.id}</td>
      <td class="mono">${fmtTime(l.probed_at)}</td>
      <td><a class="btn link" href="#/device/${l.device_id}">${esc(names[l.device_id] || "#" + l.device_id)}</a></td>
      <td><span class="badge ${l.success ? "ok" : "fail"}">${l.success ? "成功" : "失败"}</span></td>
      <td>${fmtMs(l.latency_ms)}</td>
      <td class="wrap mono">${esc(l.detail)}</td>
    </tr>`).join("") || `<tr><td colspan="6" class="loading">无记录</td></tr>`;
}

/* ---------- 设备表单 ---------- */

const TYPE_METHOD = { host: "ping", http: "curl", tcp: "tcp", dns: "dns" };

function openModal(html) {
  $("#modal").innerHTML = html;
  $("#modal-mask").classList.remove("hidden");
}
function closeModal() { $("#modal-mask").classList.add("hidden"); }

function openDeviceForm(id) {
  const isEdit = !!id;
  const load = isEdit ? api(`/api/devices/${id}`) : Promise.resolve(null);
  load.then(dev => {
    openModal(`
    <h2>${isEdit ? "编辑设备" : "添加设备"}</h2>
    <form id="device-form" onsubmit="return ui.submitDevice(event, ${id || "null"})">
      <div class="form-grid">
        <div class="field"><label>名称 *</label><input name="name" required value="${esc(dev?.name || "")}" placeholder="如：核心交换机"></div>
        <div class="field"><label>类型 *</label>
          <select name="type" id="f-type" onchange="ui.onTypeChange()">
            ${Object.keys(TYPE_METHOD).map(t => `<option value="${t}" ${dev?.type === t ? "selected" : ""}>${t}</option>`).join("")}
          </select>
        </div>
        <div class="field full"><label>目标 *（IP / URL / host:port / 域名）</label>
          <input name="target" required value="${esc(dev?.target || "")}" placeholder="如 192.168.1.1 / https://api.example.com/health / db:3306"></div>
        <div class="field"><label>探活方式</label>
          <select name="method" id="f-method">
            ${["ping", "curl", "tcp", "dns"].map(m => `<option value="${m}" ${dev?.method === m ? "selected" : ""}>${m}</option>`).join("")}
          </select>
        </div>
        <div class="field"><label>探测周期（秒）</label><input name="interval" type="number" min="5" value="${dev?.interval || 30}"></div>
        <div class="field"><label>超时（秒）</label><input name="timeout" type="number" min="1" value="${dev?.timeout || 5}"></div>
        <div class="field"><label>失败重试次数</label><input name="retries" type="number" min="0" value="${dev?.retries ?? 2}"></div>
        <div class="field full"><label>期望条件 Expect（JSON，可选）</label>
          <input name="expect" class="mono" value="${esc(dev?.expect || "")}" placeholder='如 {"status_codes":[200],"body_contains":"ok"} / {"ips":["1.2.3.4"]}'></div>
        <div class="field"><label>标签（逗号分隔）</label><input name="tags" value="${esc(dev?.tags || "")}" placeholder="生产,机房A"></div>
        <div class="field"><label>启用</label><div class="checkbox" style="margin-top:6px">
          <input type="checkbox" name="enabled" ${!dev || dev.enabled ? "checked" : ""}> 参与周期探测
        </div></div>
      </div>
      <div class="actions">
        <button type="button" class="btn" onclick="ui.closeModal()">取消</button>
        <button type="submit" class="btn primary">${isEdit ? "保存" : "创建"}</button>
      </div>
    </form>`);
    if (!isEdit) onTypeChange();
  }).catch(e => toast(e.message, true));
}

function onTypeChange() {
  const t = $("#f-type").value;
  $("#f-method").value = TYPE_METHOD[t];
}

async function submitDevice(ev, id) {
  ev.preventDefault();
  const f = ev.target;
  const payload = {
    name: f.name.value.trim(),
    type: f.type.value,
    method: f.method.value,
    target: f.target.value.trim(),
    interval: +f.interval.value || 30,
    timeout: +f.timeout.value || 5,
    retries: +f.retries.value || 0,
    expect: f.expect.value.trim(),
    tags: f.tags.value.trim(),
    enabled: f.enabled.checked,
  };
  try {
    if (id) {
      await api(`/api/devices/${id}`, { method: "PUT", body: JSON.stringify(payload) });
      toast("已保存");
    } else {
      await api("/api/devices", { method: "POST", body: JSON.stringify(payload) });
      toast("设备已创建");
    }
    closeModal();
    route();
  } catch (e) {
    toast("保存失败：" + e.message, true);
  }
  return false;
}

async function deleteDevice(id, name) {
  if (!confirm(`确定删除设备「${name}」？其探测历史将一并删除。`)) return;
  try {
    await api(`/api/devices/${id}`, { method: "DELETE" });
    toast("已删除");
    route();
  } catch (e) {
    toast(e.message, true);
  }
}

async function testDevice(id, btn) {
  if (btn) { btn.disabled = true; btn.textContent = "探测中…"; }
  try {
    const res = await api(`/api/devices/${id}/test`, { method: "POST" });
    toast(res.success
      ? `探测成功 ${fmtMs(res.latency_ms)}：${res.detail}`
      : `探测失败：${res.detail}`, !res.success);
    if (location.hash.startsWith("#/device/")) route();
  } catch (e) {
    toast(e.message, true);
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = "测试"; }
  }
}

/* ---------- 暴露给 HTML 的入口 ---------- */

window.ui = {
  openDeviceForm, closeModal, onTypeChange, submitDevice,
  deleteDevice, testDevice, queryLogs,
};
