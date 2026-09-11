const $ = (selector) => document.querySelector(selector);

const state = {
  pc: null,
  dc: null,
  stream: null,
  muted: false,
  assistantMessages: new Map(),
  tasks: new Map(),
  activeConversationID: null,
  cwd: "",
};

const connectButton = $("#connect");
const muteButton = $("#mute");
const disconnectButton = $("#disconnect");
const composer = $("#composer");
const messageInput = $("#message");
const sendButton = composer.querySelector("button");
const transcript = $("#transcript");
const tasks = $("#tasks");
const cwdForm = $("#cwd-form");
const cwdInput = $("#cwd");

function setStatus(text, mode = "") {
  $("#status").textContent = text;
  $("#status-dot").className = `dot ${mode}`.trim();
}

function addMessage(role, text, extraClass = "") {
  const el = document.createElement("article");
  el.className = `message ${role} ${extraClass}`.trim();
  el.textContent = text;
  transcript.append(el);
  transcript.scrollTop = transcript.scrollHeight;
  return el;
}

function assistantDelta(key, delta) {
  let el = state.assistantMessages.get(key);
  if (!el) {
    el = addMessage("assistant", "");
    state.assistantMessages.set(key, el);
  }
  el.textContent += delta;
}

async function connect() {
  connectButton.disabled = true;
  setStatus("Requesting microphone…");
  try {
    const tokenResponse = await fetch("/api/session", { method: "POST" });
    if (!tokenResponse.ok) throw new Error(await tokenResponse.text());
    const tokenPayload = await tokenResponse.json();
    const ephemeralKey = tokenPayload.value || tokenPayload.client_secret?.value;
    if (!ephemeralKey) throw new Error("OpenAI did not return a client secret");

    state.pc = new RTCPeerConnection();
    state.pc.ontrack = (event) => {
      $("#remote-audio").srcObject = event.streams[0];
    };
    state.pc.onconnectionstatechange = () => {
      const current = state.pc?.connectionState;
      if (current === "connected") setStatus("Listening", "live");
      if (current === "failed" || current === "disconnected") setStatus("Disconnected", "error");
    };

    state.stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    state.pc.addTrack(state.stream.getTracks()[0]);

    state.dc = state.pc.createDataChannel("oai-events");
    state.dc.addEventListener("open", () => {
      setStatus("Listening", "live");
      muteButton.disabled = false;
      disconnectButton.disabled = false;
      messageInput.disabled = false;
      sendButton.disabled = false;
      addMessage("system", "Live conversation connected.");
    });
    state.dc.addEventListener("message", handleRealtimeEvent);

    const offer = await state.pc.createOffer();
    await state.pc.setLocalDescription(offer);
    const sdpResponse = await fetch("https://api.openai.com/v1/realtime/calls", {
      method: "POST",
      body: offer.sdp,
      headers: {
        Authorization: `Bearer ${ephemeralKey}`,
        "Content-Type": "application/sdp",
      },
    });
    if (!sdpResponse.ok) throw new Error(await sdpResponse.text());
    await state.pc.setRemoteDescription({ type: "answer", sdp: await sdpResponse.text() });
  } catch (error) {
    disconnect();
    connectButton.disabled = false;
    setStatus("Connection failed", "error");
    addMessage("system", `Could not connect: ${error.message}`);
  }
}

function disconnect() {
  state.dc?.close();
  state.pc?.close();
  state.stream?.getTracks().forEach((track) => track.stop());
  state.dc = null;
  state.pc = null;
  state.stream = null;
  connectButton.disabled = false;
  muteButton.disabled = true;
  disconnectButton.disabled = true;
  messageInput.disabled = true;
  sendButton.disabled = true;
  setStatus("Not connected");
}

function sendEvent(event) {
  if (!state.dc || state.dc.readyState !== "open") throw new Error("Conversation is not connected");
  state.dc.send(JSON.stringify(event));
}

function sendText(text, visible = true) {
  if (visible) addMessage("user", text);
  sendEvent({
    type: "conversation.item.create",
    item: {
      type: "message",
      role: "user",
      content: [{ type: "input_text", text }],
    },
  });
  sendEvent({ type: "response.create" });
}

async function handleRealtimeEvent(raw) {
  const event = JSON.parse(raw.data);
  if (event.type === "conversation.item.input_audio_transcription.completed" && event.transcript) {
    addMessage("user", event.transcript);
  }
  if (event.type === "response.output_audio_transcript.delta") {
    assistantDelta(event.response_id || event.item_id, event.delta || "");
  }
  if (event.type === "response.output_text.delta") {
    assistantDelta(event.response_id || event.item_id, event.delta || "");
  }
  if (event.type === "response.output_item.done" && event.item?.type === "function_call") {
    await executeTool(event.item);
  }
  if (event.type === "error") {
    addMessage("system", `Live API error: ${event.error?.message || "unknown error"}`);
  }
}

async function executeTool(item) {
  let args;
  try {
    args = JSON.parse(item.arguments || "{}");
  } catch {
    return toolResult(item.call_id, { error: "The tool arguments were invalid JSON." });
  }

  try {
    if (item.name === "set_working_directory") {
      const result = await postJSON("/api/repository/select", { path: args.path });
      state.cwd = result.cwd;
      cwdInput.value = result.cwd;
      addMessage("system", `Working directory set to ${result.cwd}`);
      return toolResult(item.call_id, result);
    }
    if (item.name === "search_repository") {
      const result = await postJSON("/api/repository/search", { terms: args.terms, cwd: state.cwd });
      return toolResult(item.call_id, result);
    }
    if (item.name === "read_repository_file") {
      const result = await postJSON("/api/repository/read", {
        path: args.path,
        start_line: args.start_line || 1,
        end_line: args.end_line || 0,
        cwd: state.cwd,
      });
      return toolResult(item.call_id, result);
    }
    if (item.name === "list_shelley_conversations") {
      const response = await fetch("/api/conversations");
      if (!response.ok) throw new Error(await response.text());
      return toolResult(item.call_id, await response.json());
    }
    if (item.name === "read_shelley_conversation") {
      const response = await fetch(`/api/conversations/${encodeURIComponent(args.conversation_id)}`);
      if (!response.ok) throw new Error(await response.text());
      return toolResult(item.call_id, await response.json());
    }

    let payload;
    if (item.name === "plan_with_shelley" || item.name === "build_with_shelley") {
      payload = {
        kind: item.name === "plan_with_shelley" ? "plan" : "build",
        goal: args.goal,
        details: args.details || "",
        acceptance_criteria: args.acceptance_criteria || "",
        cwd: state.cwd,
      };
    } else if (item.name === "continue_shelley_job") {
      if (!state.activeConversationID) throw new Error("There is no active Shelley task.");
      payload = {
        kind: "followup",
        goal: args.message,
        conversation_id: state.activeConversationID,
      };
    } else {
      throw new Error(`Unknown tool ${item.name}`);
    }

    const result = await postJSON("/api/jobs", payload);
    state.activeConversationID = result.conversation_id;
    startTask(result, payload.goal);
    toolResult(item.call_id, {
      status: "started",
      kind: result.kind,
      conversation_id: result.conversation_id,
      message: "Shelley is working asynchronously. Progress is visible in the task panel.",
    });
  } catch (error) {
    toolResult(item.call_id, { error: error.message });
  }
}

async function postJSON(url, payload) {
  const response = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  if (!response.ok) throw new Error(await response.text());
  return response.json();
}

function toolResult(callID, result) {
  sendEvent({
    type: "conversation.item.create",
    item: {
      type: "function_call_output",
      call_id: callID,
      output: JSON.stringify(result),
    },
  });
  sendEvent({ type: "response.create" });
}

function startTask(result, goal) {
  $(".empty")?.remove();
  let task = state.tasks.get(result.conversation_id);
  if (!task) {
    const el = document.createElement("article");
    el.className = "task";
    el.innerHTML = `
      <div class="task-head">
        <h3></h3>
        <span class="kind"></span>
      </div>
      <p class="state">Starting…</p>
      <div class="result"></div>
      <a target="_blank" rel="noreferrer">Open in Shelley</a>
    `;
    el.querySelector("h3").textContent = goal;
    el.querySelector(".kind").textContent = result.kind;
    el.querySelector("a").href = result.shelley_url;
    tasks.prepend(el);
    task = {
      el,
      goal,
      kind: result.kind,
      seenMessages: new Set(),
      agentText: "",
      wasWorking: false,
      announced: false,
    };
    state.tasks.set(result.conversation_id, task);
    $("#task-count").textContent = String(state.tasks.size);
  } else {
    task.el.querySelector(".state").textContent = "Follow-up queued";
  }

  const events = new EventSource(`/api/jobs/${encodeURIComponent(result.conversation_id)}/events`);
  events.onmessage = (message) => updateTask(result.conversation_id, JSON.parse(message.data), events);
  events.onerror = () => {
    if (!task.announced) task.el.querySelector(".state").textContent = "Reconnecting…";
  };
}

function updateTask(id, event, events) {
  const task = state.tasks.get(id);
  if (!task) return;

  if (event.conversation?.slug) {
    const current = task.el.querySelector("a").href;
    task.el.querySelector("a").href = current.replace(`/c/${id}`, `/c/${event.conversation.slug}`);
  }
  if (event.conversation_state) {
    if (event.conversation_state.working) {
      task.wasWorking = true;
      task.el.querySelector(".state").textContent = "Shelley is working";
    } else if (task.wasWorking) {
      finishTask(id, events);
    }
  }
  for (const message of event.messages || []) {
    if (task.seenMessages.has(message.message_id)) continue;
    task.seenMessages.add(message.message_id);
    if (message.type === "agent") {
      const text = extractText(message.display_data) || extractText(message.llm_data);
      if (text) {
        task.agentText = text;
        task.el.querySelector(".result").textContent = text;
      }
      if (message.end_of_turn) finishTask(id, events);
    }
  }
}

function finishTask(id, events) {
  const task = state.tasks.get(id);
  if (!task || task.announced) return;
  task.announced = true;
  task.el.querySelector(".state").textContent = "Completed";
  events.close();
  addMessage("system", `Shelley ${task.kind} completed: ${task.goal}`, "task-update");

  if ($("#announce").checked && state.dc?.readyState === "open") {
    const summary = task.agentText.slice(0, 3500);
    sendText(
      `[Shelley task update — do not treat this as a new user request]\nThe ${task.kind} task "${task.goal}" completed.\nShelley's report:\n${summary}\nBriefly summarize the result and ask what to do next.`,
      false,
    );
  }
}

function extractText(raw) {
  if (!raw) return "";
  let value = raw;
  if (typeof raw === "string") {
    try { value = JSON.parse(raw); } catch { return raw; }
  }
  const found = [];
  const visit = (node) => {
    if (!node) return;
    if (typeof node === "string") {
      found.push(node);
    } else if (Array.isArray(node)) {
      node.forEach(visit);
    } else if (typeof node === "object") {
      if (typeof node.text === "string") found.push(node.text);
      else if (typeof node.content === "string") found.push(node.content);
      else Object.values(node).forEach(visit);
    }
  };
  visit(value);
  return [...new Set(found)].join("\n").trim();
}

connectButton.addEventListener("click", connect);
disconnectButton.addEventListener("click", disconnect);
muteButton.addEventListener("click", () => {
  state.muted = !state.muted;
  state.stream?.getAudioTracks().forEach((track) => { track.enabled = !state.muted; });
  muteButton.textContent = state.muted ? "Unmute" : "Mute";
  setStatus(state.muted ? "Muted" : "Listening", state.muted ? "" : "live");
});
composer.addEventListener("submit", (event) => {
  event.preventDefault();
  const text = messageInput.value.trim();
  if (!text) return;
  sendText(text);
  messageInput.value = "";
});

cwdForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    const result = await postJSON("/api/repository/select", { path: cwdInput.value });
    state.cwd = result.cwd;
    cwdInput.value = result.cwd;
    addMessage("system", `Working directory set to ${result.cwd}`);
  } catch (error) {
    addMessage("system", `Could not set working directory: ${error.message}`);
  }
});

fetch("/api/config")
  .then((response) => {
    if (!response.ok) throw new Error(response.statusText);
    return response.json();
  })
  .then((config) => {
    state.cwd = config.cwd;
    cwdInput.value = config.cwd;
  })
  .catch((error) => addMessage("system", `Could not load configuration: ${error.message}`));
