// MeshRoom UI — vanilla JS SPA. Состояние приходит по SSE, действия — POST.

let S = null;            // последний снимок состояния с бэкенда
let currentRoom = null;  // id открытой комнаты
let pendingTunnel = {};  // roomId -> true, пока идёт включение

const $ = (sel, el) => (el || document).querySelector(sel);
const screen = $("#screen");

// ---------- утилиты ----------

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function toast(text, isErr) {
  const el = document.createElement("div");
  el.className = "toast" + (isErr ? " err" : "");
  el.textContent = text;
  $("#toast-root").appendChild(el);
  setTimeout(() => el.remove(), 3200);
}

async function api(path, body) {
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body || {}),
  });
  const j = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(j.error || res.statusText);
  return j;
}

function avatarHTML(name, avatar, cls) {
  const inner = avatar
    ? `<img src="${esc(avatar)}" alt="">`
    : esc((name || "?").trim().charAt(0).toUpperCase() || "?");
  return `<div class="av ${cls || ""}">${inner}</div>`;
}

function copyText(text, msg) {
  navigator.clipboard.writeText(text).then(
    () => toast(msg || t("copied")),
    () => {
      const ta = document.createElement("textarea");
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      ta.remove();
      toast(msg || t("copied"));
    }
  );
}

// уменьшение выбранной картинки до аватарки 128x128 (dataURL)
function pickAvatar(cb) {
  const inp = document.createElement("input");
  inp.type = "file";
  inp.accept = "image/*";
  inp.onchange = () => {
    const f = inp.files[0];
    if (!f) return;
    const img = new Image();
    img.onload = () => {
      const c = document.createElement("canvas");
      c.width = c.height = 128;
      const ctx = c.getContext("2d");
      const s = Math.min(img.width, img.height);
      ctx.drawImage(img, (img.width - s) / 2, (img.height - s) / 2, s, s, 0, 0, 128, 128);
      cb(c.toDataURL("image/jpeg", 0.82));
    };
    img.src = URL.createObjectURL(f);
  };
  inp.click();
}

// ---------- модальные окна ----------

function modal(html) {
  const root = $("#modal-root");
  root.innerHTML = `<div class="overlay"><div class="modal glass">${html}</div></div>`;
  root.firstChild.addEventListener("mousedown", (e) => {
    if (e.target === root.firstChild) closeModal();
  });
  return root.firstChild;
}
function closeModal() { $("#modal-root").innerHTML = ""; }

// ---------- экраны ----------

function render() {
  $("#langBtn").textContent = LANG === "ru" ? "EN" : "RU";
  if (!S) return;
  if (!S.profileExists) return renderOnboarding();
  if (!S.profileUnlocked) return renderUnlock();
  renderMain();
}

function renderOnboarding() {
  let avatar = "";
  screen.innerHTML = `
    <div class="center-wrap"><div class="auth-card glass">
      <h1>${t("onboardingTitle")}</h1>
      <div class="sub">${t("onboardingSub")}</div>
      <div class="avatar-pick">
        <span id="obAv">${avatarHTML("", "", "")}</span>
        <div class="hint">${t("avatarHint")}</div>
      </div>
      <label class="fld"><span>${t("nickname")}</span><input id="obName" type="text" maxlength="24"></label>
      <label class="fld"><span>${t("password")}</span><input id="obPass" type="password"></label>
      <label class="fld"><span>${t("passwordRepeat")}</span><input id="obPass2" type="password"></label>
      <div class="actions"><button class="primary-btn" id="obGo">${t("create")}</button></div>
    </div></div>`;
  $("#obAv").onclick = () => pickAvatar((d) => {
    avatar = d;
    $("#obAv").innerHTML = avatarHTML("", d, "");
  });
  $("#obGo").onclick = async () => {
    const name = $("#obName").value.trim();
    const p1 = $("#obPass").value, p2 = $("#obPass2").value;
    if (!name) return toast(t("errNeedName"), true);
    if (!p1) return toast(t("errNeedPass"), true);
    if (p1 !== p2) return toast(t("errPassMismatch"), true);
    try { await api("/api/profile/create", { name, avatar, password: p1 }); }
    catch (e) { toast(e.message, true); }
  };
}

function renderUnlock() {
  screen.innerHTML = `
    <div class="center-wrap"><div class="auth-card glass">
      <div class="avatar-pick">${avatarHTML(S.name, S.avatar)}<div><b>${esc(S.name)}</b></div></div>
      <h1>${t("unlockTitle")}</h1>
      <div class="sub">${t("unlockSub")}</div>
      <label class="fld"><span>${t("password")}</span><input id="ulPass" type="password" autofocus></label>
      <div class="actions"><button class="primary-btn" id="ulGo">${t("unlock")}</button></div>
    </div></div>`;
  const go = async () => {
    try { await api("/api/profile/unlock", { password: $("#ulPass").value }); }
    catch (e) { toast(e.message, true); }
  };
  $("#ulGo").onclick = go;
  $("#ulPass").onkeydown = (e) => { if (e.key === "Enter") go(); };
  $("#ulPass").focus();
}

function renderMain() {
  const rooms = S.rooms || [];
  if (currentRoom && !rooms.find((r) => r.id === currentRoom)) currentRoom = null;
  if (!currentRoom && rooms.length) currentRoom = rooms[0].id;

  screen.innerHTML = `
    <div class="layout">
      <aside class="sidebar glass">
        <div class="side-head"><h2>${t("rooms")}</h2></div>
        <div class="room-list" id="roomList">
          ${rooms.length ? rooms.map(roomItemHTML).join("") : `<div class="empty" style="height:auto;padding:30px 8px;white-space:pre-line">${t("noRooms")}</div>`}
        </div>
        <div class="side-actions">
          <button id="btnCreate">＋ ${t("createRoom")}</button>
          <button id="btnJoin">⇥ ${t("joinRoom")}</button>
        </div>
        <div class="me-chip" id="meChip">
          ${avatarHTML(S.name, S.avatar, "small")}
          <div><div class="mename">${esc(S.name)}</div><div class="mekey">${esc((S.pubkey || "").slice(0, 12))}…</div></div>
        </div>
      </aside>
      <section class="room-panel glass" id="roomPanel"></section>
    </div>`;

  $("#btnCreate").onclick = createRoomModal;
  $("#btnJoin").onclick = joinRoomModal;
  $("#meChip").onclick = profileModal;
  for (const el of screen.querySelectorAll(".room-item")) {
    el.onclick = () => { currentRoom = el.dataset.id; render(); };
  }
  renderRoomPanel();
}

function roomItemHTML(r) {
  const on = r.role === "host" ? true : r.connected;
  const cnt = (r.peers || []).filter((p) => p.online).length;
  return `<div class="room-item ${r.id === currentRoom ? "active" : ""}" data-id="${r.id}">
    <div class="rname"><span class="dot ${on ? "on" : "off"}"></span>${esc(r.name)}</div>
    <div class="rmeta">${esc(r.myIp || "")} · ${cnt} ${t("online")}</div>
  </div>`;
}

function renderRoomPanel() {
  const panel = $("#roomPanel");
  const r = (S.rooms || []).find((x) => x.id === currentRoom);
  if (!r) {
    panel.innerHTML = `<div class="empty"><div><div class="big">◈</div><div style="white-space:pre-line">${t("selectRoom")}</div></div></div>`;
    return;
  }
  const pending = pendingTunnel[r.id];
  const tState = r.tunnelErr
    ? `<span class="tunnel-state err" title="${esc(r.tunnelErr)}">${esc(r.tunnelErr).slice(0, 60)}</span>`
    : `<span class="tunnel-state ${r.tunnelOn ? "on" : ""}">${pending ? t("tunnelConnecting") : r.tunnelOn ? t("tunnelOn") : t("tunnelOff")}</span>`;

  panel.innerHTML = `
    <div class="room-head">
      <h2>${esc(r.name)}</h2>
      <span class="subnet">${esc(r.subnet)}</span>
      <span class="subnet">· ${r.role === "host" ? t("hostRole") : r.connected ? t("connected") : t("disconnected")}</span>
      <div class="spacer"></div>
      ${tState}
      <label class="switch" title="VPN">
        <input type="checkbox" id="tunSw" ${r.tunnelOn ? "checked" : ""} ${pending ? "disabled" : ""}>
        <div class="track"></div><div class="thumb"></div>
      </label>
      ${r.role === "host" ? `<button id="btnInvite">${t("invite")}</button>` : ""}
      <button class="danger-btn" id="btnLeave">${t("leave")}</button>
    </div>
    <div class="room-body">
      <div class="members">
        ${reachBanner(r)}
        <h3>${t("members")} · ${(r.peers || []).length}</h3>
        ${(r.peers || []).map((p) => memberHTML(r, p)).join("")}
      </div>
      <div class="chat">
        <h3>${t("chat")}</h3>
        <div class="chat-log" id="chatLog">${(r.chat || []).map(msgHTML).join("")}</div>
        <div class="chat-input">
          <input id="chatText" type="text" placeholder="${t("chatPlaceholder")}" maxlength="2000">
          <button class="primary-btn" id="chatSend">${t("send")}</button>
        </div>
      </div>
    </div>`;

  const log = $("#chatLog");
  log.scrollTop = log.scrollHeight;

  $("#tunSw").onchange = async (e) => {
    const on = e.target.checked;
    pendingTunnel[r.id] = true;
    renderRoomPanel();
    try { await api("/api/room/tunnel", { roomId: r.id, on }); }
    catch (err) { toast(err.message, true); }
    delete pendingTunnel[r.id];
  };
  if ($("#btnInvite")) $("#btnInvite").onclick = () => inviteModal(r.id);
  $("#btnLeave").onclick = async () => {
    if (!confirm(r.role === "host" ? t("deleteRoomConfirm") : t("leaveConfirm"))) return;
    try { await api("/api/room/leave", { roomId: r.id }); currentRoom = null; }
    catch (e) { toast(e.message, true); }
  };
  const sendMsg = async () => {
    const text = $("#chatText").value.trim();
    if (!text) return;
    $("#chatText").value = "";
    try { await api("/api/room/chat", { roomId: r.id, text }); }
    catch (e) { toast(e.message, true); }
  };
  $("#chatSend").onclick = sendMsg;
  $("#chatText").onkeydown = (e) => { if (e.key === "Enter") sendMsg(); };
  for (const el of panel.querySelectorAll(".mip")) {
    el.onclick = () => copyText(el.textContent, t("ipCopied"));
  }
  for (const el of panel.querySelectorAll(".kick")) {
    el.onclick = () => api("/api/room/kick", { roomId: r.id, pubkey: el.dataset.pk }).catch((e) => toast(e.message, true));
  }
}

// connLabel — текст и класс индикатора соединения по полю conn из бэкенда.
function connLabel(p) {
  if (p.isSelf) return { text: t("you"), cls: "" };
  switch (p.conn) {
    case "direct": return { text: t("direct"), cls: "c-direct" };
    case "relay": return { text: t("viaRelay"), cls: "c-relay" };
    case "offline": return { text: t("offline"), cls: "c-off" };
    default: return { text: p.online ? t("online") : t("offline"), cls: p.online ? "" : "c-off" };
  }
}

function memberHTML(r, p) {
  const c = connLabel(p);
  const dotCls = p.conn === "direct" ? "on" : p.conn === "relay" ? "relay" : p.online ? "on" : "off";
  return `<div class="member">
    ${avatarHTML(p.name, p.avatar, "small")}
    <div>
      <div class="mname">${esc(p.name)}${p.isHost ? ` <span class="badge">${t("host")}</span>` : ""}${p.isSelf ? ` <span class="badge">${t("you")}</span>` : ""}</div>
      <div class="mip" title="copy">${esc(p.ip || "—")}</div>
    </div>
    <div class="mstat">
      <span class="dot ${dotCls}"></span><span class="${c.cls}">${c.text}</span>
      ${r.role === "host" && !p.isSelf ? `<button class="kick ghost-btn" data-pk="${esc(p.pubkey)}">${t("kick")}</button>` : ""}
    </div>
  </div>`;
}

// reachBanner — плашка диагностики доступности порта хоста снаружи.
function reachBanner(r) {
  if (r.role !== "host") return "";
  if (!r.reachable) {
    return `<div class="reach">${t("reachChecking")}</div>`;
  }
  if (r.reachable === "ok") {
    return `<div class="reach ok">✓ ${t("reachOk")}</div>`;
  }
  const text = t("reachBlocked").replace("{port}", r.ctlPort || "?");
  return `<div class="reach warn">⚠ ${esc(text)}</div>`;
}

function msgHTML(m) {
  const mine = S && m.from === S.pubkey;
  const time = new Date(m.timeMs).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  return `<div class="msg ${mine ? "mine" : ""}">
    <div class="mhead"><span>${esc(m.name)}</span><span>${time}</span></div>
    <div class="mtext">${esc(m.text)}</div>
  </div>`;
}

// ---------- модалки действий ----------

function createRoomModal() {
  modal(`
    <h2>${t("createRoom")}</h2>
    <label class="fld"><span>${t("roomName")}</span><input id="crName" type="text" maxlength="40"></label>
    <div class="actions">
      <button class="ghost-btn" onclick="closeModal()">${t("cancel")}</button>
      <button class="primary-btn" id="crGo">${t("create")}</button>
    </div>`);
  $("#crName").focus();
  $("#crGo").onclick = async () => {
    try {
      const j = await api("/api/room/create", { name: $("#crName").value.trim() });
      currentRoom = j.roomId;
      closeModal();
      inviteModal(j.roomId);
    } catch (e) { toast(e.message, true); }
  };
}

function joinRoomModal() {
  modal(`
    <h2>${t("joinRoom")}</h2>
    <label class="fld"><span>${t("inviteLink")}</span><textarea id="jnLink" rows="4" placeholder="meshroom://join?…"></textarea></label>
    <div class="actions">
      <button class="ghost-btn" onclick="closeModal()">${t("cancel")}</button>
      <button class="primary-btn" id="jnGo">${t("join")}</button>
    </div>`);
  $("#jnLink").focus();
  $("#jnGo").onclick = async () => {
    const btn = $("#jnGo");
    btn.disabled = true;
    try {
      const j = await api("/api/room/join", { invite: $("#jnLink").value });
      currentRoom = j.roomId;
      closeModal();
    } catch (e) { toast(e.message, true); btn.disabled = false; }
  };
}

async function inviteModal(roomId) {
  try {
    const j = await api("/api/room/invite", { roomId });
    modal(`
      <h2>${t("inviteTitle")}</h2>
      <div class="sub" style="color:var(--text-dim);font-size:13px;margin-bottom:12px">${t("inviteHint")}</div>
      <div class="invite-box">${esc(j.invite)}</div>
      <div class="actions">
        <button class="ghost-btn" onclick="closeModal()">${t("close")}</button>
        <button class="primary-btn" id="invCopy">${t("copy")}</button>
      </div>`);
    $("#invCopy").onclick = () => copyText(j.invite);
  } catch (e) { toast(e.message, true); }
}

function profileModal() {
  let avatar = S.avatar || "";
  modal(`
    <h2>${t("profileTitle")}</h2>
    <div class="avatar-pick">
      <span id="pfAv">${avatarHTML(S.name, avatar)}</span>
      <div class="hint">${t("avatarHint")}</div>
    </div>
    <label class="fld"><span>${t("nickname")}</span><input id="pfName" type="text" maxlength="24" value="${esc(S.name)}"></label>
    <div class="actions">
      <button class="ghost-btn" onclick="closeModal()">${t("cancel")}</button>
      <button class="primary-btn" id="pfGo">${t("save")}</button>
    </div>`);
  $("#pfAv").onclick = () => pickAvatar((d) => {
    avatar = d;
    $("#pfAv").innerHTML = avatarHTML(S.name, d);
  });
  $("#pfGo").onclick = async () => {
    try {
      await api("/api/profile/update", { name: $("#pfName").value.trim(), avatar });
      closeModal();
    } catch (e) { toast(e.message, true); }
  };
}

// ---------- события ----------

function connectSSE() {
  const es = new EventSource("/api/events");
  es.addEventListener("state", (e) => {
    S = JSON.parse(e.data);
    render();
  });
  es.addEventListener("chat", (e) => {
    const { roomId, msg } = JSON.parse(e.data);
    const r = S && (S.rooms || []).find((x) => x.id === roomId);
    if (r) {
      r.chat = r.chat || [];
      r.chat.push(msg);
    }
    if (roomId === currentRoom && $("#chatLog")) {
      $("#chatLog").insertAdjacentHTML("beforeend", msgHTML(msg));
      $("#chatLog").scrollTop = $("#chatLog").scrollHeight;
    }
  });
  es.onerror = () => {
    es.close();
    setTimeout(connectSSE, 1500);
  };
}

$("#langBtn").onclick = () => { toggleLang(); render(); };
connectSSE();
