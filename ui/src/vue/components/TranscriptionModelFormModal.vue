<!-- Stacked form for a custom transcription provider. The catalog never sends
     secrets back to the browser; an empty API-key field retains an existing
     stored key while an explicit value replaces it. -->
<template>
  <Modal
    :is-open="isOpen"
    :title="editModel ? 'Edit Transcription Model' : 'Add Transcription Model'"
    class-name="modal-wide"
    @close="close"
  >
    <div class="model-form transcription-model-form">
      <div v-if="error" class="models-error" role="alert">{{ error }}</div>
      <div class="form-group">
        <label id="transcription-protocol-label">Protocol</label>
        <div
          class="provider-buttons"
          role="radiogroup"
          aria-labelledby="transcription-protocol-label"
        >
          <label
            v-for="protocol in protocols"
            :key="protocol.value"
            :class="['provider-btn', { selected: form.protocol === protocol.value }]"
          >
            <input
              v-model="form.protocol"
              type="radio"
              name="transcription-protocol"
              :value="protocol.value"
            />
            <span>{{ protocol.label }}</span>
          </label>
        </div>
      </div>
      <div class="form-group">
        <label for="transcription-provider">Provider</label
        ><InputText
          id="transcription-provider"
          v-model="form.provider"
          placeholder="e.g., Deepgram"
          fluid
          :dt="inputFieldDt"
        />
      </div>
      <div class="form-group">
        <label for="transcription-endpoint">Endpoint</label
        ><InputText
          id="transcription-endpoint"
          v-model="form.endpoint"
          placeholder="https://..."
          fluid
          :dt="inputFieldDt"
        />
      </div>
      <div class="form-group">
        <label for="transcription-model-id">Model ID</label
        ><InputText
          id="transcription-model-id"
          v-model="form.model_name"
          :placeholder="modelIdPlaceholder"
          fluid
          :dt="inputFieldDt"
        />
      </div>
      <div v-if="!isNativeDeepgram" class="form-group">
        <label for="transcription-api-behavior">API behavior</label>
        <Select
          v-model="form.api_profile"
          input-id="transcription-api-behavior"
          :options="apiBehaviors"
          option-label="label"
          option-value="value"
          aria-label="API behavior"
          aria-describedby="transcription-api-behavior-help"
          fluid
          :dt="selectFieldDt"
        />
        <p
          id="transcription-api-behavior-help"
          class="form-hint"
          role="status"
          aria-live="polite"
        >
          {{ behaviorHelpText }}
        </p>
      </div>
      <div v-if="!isNativeDeepgram" class="form-group">
        <label for="transcription-request-encoding">Request encoding</label>
        <Select
          v-model="form.request_encoding"
          input-id="transcription-request-encoding"
          :options="requestEncodings"
          option-label="label"
          option-value="value"
          aria-label="Request encoding"
          aria-describedby="transcription-request-encoding-help"
          fluid
          :dt="selectFieldDt"
        />
        <p
          id="transcription-request-encoding-help"
          class="form-hint"
          role="status"
          aria-live="polite"
        >
          {{ requestEncodingHelpText }}
        </p>
      </div>
      <div class="form-group">
        <label for="transcription-display-name">Name</label
        ><InputText
          id="transcription-display-name"
          v-model="form.display_name"
          placeholder="Name shown in this table"
          fluid
          :dt="inputFieldDt"
        />
      </div>
      <div class="form-group">
        <label for="transcription-api-key"
          >API Key
          <span class="optional">{{
            editModel?.has_api_key ? "(leave blank to keep saved key)" : ""
          }}</span></label
        >
        <InputText
          id="transcription-api-key"
          v-model="form.api_key"
          type="password"
          :placeholder="editModel?.has_api_key ? 'Saved server-side' : 'Enter API key'"
          autocomplete="off"
          fluid
          :dt="inputFieldDt"
        />
      </div>
      <fieldset
        class="transcription-capabilities"
        :aria-describedby="isNativeDeepgram ? undefined : 'transcription-api-behavior-help'"
      >
        <legend>Capabilities</legend>
        <label
          ><input
            v-model="form.supports_prompted"
            type="checkbox"
            :disabled="contextPromptsLocked"
          />
          Context prompts</label
        >
        <label
          ><input
            v-model="form.supports_timecodes"
            type="checkbox"
            :disabled="timecodesLocked"
          />
          Timecodes</label
        >
        <p v-if="capabilityHint" class="form-hint transcription-capability-hint">
          {{ capabilityHint }}
        </p>
      </fieldset>

      <section class="transcription-test" aria-label="Connection test">
        <div class="transcription-test-heading">Test with a built-in spoken sample</div>
        <p class="form-hint">No microphone needed. The draft is sent only for this test.</p>
        <div v-if="testState === 'processing'" class="transcription-test-recording" role="status">
          <span class="spinner spinner-small" />
          Testing transcription…
        </div>
        <div v-else-if="testState === 'success'" class="test-result success" role="status">
          ✓ Test successful
        </div>
        <div v-else-if="testState === 'failure'" class="test-result error" role="alert">
          ✗ {{ testFailure }}
        </div>
        <div v-if="testResult" class="transcription-test-results">
          <div v-if="testResult.results.transcript" class="transcription-test-result">
            <strong>Transcript</strong
            ><span :class="{ 'test-result-failure': !testResult.results.transcript.success }">{{
              testResult.results.transcript.success
                ? testResult.results.transcript.transcript || "No transcript returned."
                : testResult.results.transcript.message
            }}</span>
          </div>
          <div v-if="testResult.results.timecoded" class="transcription-test-result">
            <strong>Timecoded transcription</strong
            ><span :class="{ 'test-result-failure': !testResult.results.timecoded.success }">{{
              testResult.results.timecoded.success
                ? testResult.results.timecoded.has_timecodes
                  ? "Timecodes returned."
                  : "No timecodes returned."
                : testResult.results.timecoded.message
            }}</span>
          </div>
        </div>
        <div class="transcription-test-actions">
          <Button
            v-if="testState !== 'processing'"
            type="button"
            severity="secondary"
            label="Test model"
            :disabled="!canTest"
            @click="testModel"
          />
          <Button
            v-if="testState === 'processing'"
            type="button"
            severity="secondary"
            label="Cancel test"
            @click="cancelTest"
          />
        </div>
      </section>

      <div class="form-actions">
        <Button
          type="button"
          severity="secondary"
          label="Cancel"
          :disabled="isBusy"
          @click="close"
        />
        <Button
          type="button"
          :label="editModel ? 'Save' : 'Add Model'"
          :disabled="isBusy"
          @click="save"
        />
      </div>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from "vue";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import Select from "primevue/select";
import Modal from "./Modal.vue";
import { inputFieldDt, selectFieldDt } from "./configFieldDt";
import {
  transcriptionModelsApi,
  type TranscriptionModel,
  type TranscriptionModelDraft,
  type TranscriptionModelTestResult,
  type TranscriptionAPIProfile,
  type TranscriptionRequestEncoding,
  type TranscriptionProtocol,
} from "../../services/api";

const props = defineProps<{ isOpen: boolean; editModel: TranscriptionModel | null }>();
const emit = defineEmits<{ (e: "close"): void; (e: "saved"): void }>();
type TestState = "idle" | "processing" | "success" | "failure";
const protocols: { value: TranscriptionProtocol; label: string }[] = [
  { value: "openai", label: "OpenAI-compatible" },
  { value: "deepgram", label: "Deepgram" },
];
const protocolDefaults: Record<
  TranscriptionProtocol,
  {
    display_name: string;
    protocol: TranscriptionProtocol;
    provider: string;
    endpoint: string;
    model_name: string;
    api_profile: TranscriptionAPIProfile;
    request_encoding: TranscriptionRequestEncoding;
    supports_prompted: boolean;
    supports_timecodes: boolean;
  }
> = {
  openai: {
    display_name: "",
    protocol: "openai",
    provider: "OpenAI",
    endpoint: "https://api.openai.com/v1/audio/transcriptions",
    model_name: "",
    api_profile: "auto",
    request_encoding: "auto",
    supports_prompted: true,
    supports_timecodes: false,
  },
  deepgram: {
    display_name: "",
    protocol: "deepgram",
    provider: "Deepgram",
    endpoint: "https://api.deepgram.com/v1/listen",
    model_name: "",
    api_profile: "auto",
    request_encoding: "auto",
    supports_prompted: false,
    supports_timecodes: true,
  },
};
const apiBehaviors: { value: TranscriptionAPIProfile; label: string }[] = [
  { value: "auto", label: "Auto-detect" },
  { value: "openai-json", label: "Standard transcription" },
  { value: "openai-whisper", label: "Whisper timestamps" },
  { value: "openai-diarized", label: "Speaker diarization" },
];
const requestEncodings: { value: TranscriptionRequestEncoding; label: string }[] = [
  { value: "auto", label: "Auto-detect" },
  { value: "multipart", label: "Multipart file" },
  { value: "base64-json", label: "Base64 JSON" },
];
const form = reactive({
  api_key: "",
  ...protocolDefaults.openai,
});
const error = ref("");
const testState = ref<TestState>("idle");
const testFailure = ref("");
const testResult = ref<TranscriptionModelTestResult | null>(null);
const loadingForm = ref(false);
let testAbort: AbortController | null = null;
let testGeneration = 0;

const isNativeDeepgram = computed(() => form.protocol === "deepgram");
function inferredAutoProfile(modelName: string): Exclude<TranscriptionAPIProfile, "auto"> | null {
  const normalized = modelName.trim().toLowerCase();
  const basename = normalized.replace(/\/+$/, "").split("/").pop() || ".";
  if (basename.includes("diarize")) return "openai-diarized";
  if (basename.includes("whisper")) return "openai-whisper";
  if (
    basename === "gpt-4o-transcribe" ||
    basename.startsWith("gpt-4o-transcribe-") ||
    basename === "gpt-4o-mini-transcribe" ||
    basename.startsWith("gpt-4o-mini-transcribe-") ||
    basename === "gpt-transcribe" ||
    basename.startsWith("gpt-transcribe-")
  ) {
    return "openai-json";
  }
  return null;
}
const resolvedOpenAIProfile = computed(() =>
  form.api_profile === "auto" ? inferredAutoProfile(form.model_name) : form.api_profile,
);
const inferredRequestEncoding = computed<Exclude<TranscriptionRequestEncoding, "auto">>(() =>
  form.model_name.trim().toLowerCase().startsWith("fish-audio/")
    ? "base64-json"
    : "multipart",
);
const resolvedRequestEncoding = computed(() =>
  form.request_encoding === "auto" ? inferredRequestEncoding.value : form.request_encoding,
);
const requestEncodingHelpText = computed(() => {
  if (form.request_encoding === "auto") {
    return resolvedRequestEncoding.value === "base64-json"
      ? "Detected: Base64 JSON for Fish Audio."
      : "Detected: multipart file upload.";
  }
  return form.request_encoding === "base64-json"
    ? "Sends WAV audio as base64 JSON; select Whisper timestamps when the model supports timecodes."
    : "Uploads the recording as an OpenAI-compatible multipart file.";
});
const contextPromptsLocked = computed(
  () =>
    isNativeDeepgram.value ||
    resolvedRequestEncoding.value === "base64-json" ||
    resolvedOpenAIProfile.value === "openai-diarized",
);
const timecodesLocked = computed(() => resolvedOpenAIProfile.value === "openai-json");
const capabilityHint = computed(() => {
  if (isNativeDeepgram.value)
    return "Native Deepgram supports Transcript and optional timecodes, but not Context prompts.";
  if (resolvedRequestEncoding.value === "base64-json")
    return "Base64 JSON supports Transcript, but not Context prompts.";
  if (resolvedOpenAIProfile.value === "openai-diarized")
    return "Speaker diarization supports Transcript and optional timecodes, but not Context prompts.";
  if (resolvedOpenAIProfile.value === "openai-json")
    return "Standard transcription supports Transcript and optional Context prompts, but not timecodes.";
  return "";
});
const autoBehaviorText = computed(() => {
  switch (resolvedOpenAIProfile.value) {
    case "openai-json":
      return "Detected: standard transcription with optional Context prompts.";
    case "openai-whisper":
      return "Detected: Whisper timestamps (both capabilities).";
    case "openai-diarized":
      return "Detected: speaker diarization without Context prompts.";
    default:
      return "Custom or legacy model: choose capabilities manually.";
  }
});
const behaviorHelpText = computed(() => {
  if (form.api_profile === "auto") return autoBehaviorText.value;
  switch (form.api_profile) {
    case "openai-json":
      return "Uses JSON responses for transcript generation and optional Context prompts.";
    case "openai-whisper":
      return "Uses verbose JSON timestamps; choose either capability.";
    case "openai-diarized":
      return "Uses speaker-labeled segments for timecoded transcription.";
  }
});
const modelIdPlaceholder = computed(() =>
  form.protocol === "deepgram" ? "e.g., nova-3" : "e.g., whisper-1",
);
const isBusy = computed(
  () => testState.value === "processing",
);
const canTest = computed(() => validDraft(false) === null);

function isCurrentTest(generation: number) {
  return props.isOpen && generation === testGeneration;
}

function reset() {
  error.value = "";
  testState.value = "idle";
  testFailure.value = "";
  testResult.value = null;
}

function cancelTest() {
  testGeneration += 1;
  testAbort?.abort();
  testAbort = null;
  testResult.value = null;
  testFailure.value = "";
  testState.value = "idle";
}

function draft(includeKey = true): TranscriptionModelDraft {
  const value: TranscriptionModelDraft = {
    model_id: props.editModel?.model_id,
    display_name: form.display_name.trim(),
    protocol: form.protocol,
    provider: form.provider.trim(),
    endpoint: form.endpoint.trim(),
    model_name: form.model_name.trim(),
    api_profile: isNativeDeepgram.value ? "auto" : form.api_profile,
    request_encoding: isNativeDeepgram.value ? "auto" : form.request_encoding,
    supports_prompted: contextPromptsLocked.value ? false : form.supports_prompted,
    supports_timecodes: timecodesLocked.value ? false : form.supports_timecodes,
  };
  if (includeKey && form.api_key) value.api_key = form.api_key;
  return value;
}

function validDraft(requireKey: boolean): string | null {
  const value = draft(false);
  if (!value.display_name || !value.provider || !value.endpoint || !value.model_name)
    return "Name, provider, endpoint, and model ID are required.";

  try {
    const url = new URL(value.endpoint);
    if (url.protocol !== "https:" && url.protocol !== "http:")
      return "Endpoint must be an absolute HTTP or HTTPS URL.";
  } catch {
    return "Endpoint must be an absolute HTTP or HTTPS URL.";
  }
  if (requireKey && !form.api_key && !props.editModel?.has_api_key) return "API key is required.";
  return null;
}

async function testModel() {
  if (isBusy.value) return;
  const invalid = validDraft(true);
  if (invalid) {
    testState.value = "failure";
    testFailure.value = invalid;
    return;
  }
  reset();
  const generation = ++testGeneration;
  testState.value = "processing";
  const abort = new AbortController();
  testAbort = abort;
  try {
    const sampleResponse = await fetch("/transcription-test.wav", { signal: abort.signal });
    if (!sampleResponse.ok) throw new Error("Could not load the transcription test sample.");
    const sample = new Blob([await sampleResponse.arrayBuffer()], { type: "audio/wav" });
    const result = await transcriptionModelsApi.test(draft(), sample, abort.signal);
    if (!isCurrentTest(generation) || abort.signal.aborted) return;
    testResult.value = result;
    const failures = Object.values(result.results).some((result) => result && !result.success);
    testState.value = failures ? "failure" : "success";
    if (failures) testFailure.value = "One or more requested transcription tests failed.";
  } catch (cause) {
    if (!isCurrentTest(generation) || abort.signal.aborted) return;
    testState.value = "failure";
    testFailure.value = cause instanceof Error ? cause.message : "Test failed.";
  } finally {
    if (testAbort === abort) testAbort = null;
  }
}
async function save() {
  const invalid = validDraft(true);
  if (invalid) {
    error.value = invalid;
    return;
  }
  try {
    error.value = "";
    const value = draft();
    if (props.editModel) await transcriptionModelsApi.update(props.editModel.model_id, value);
    else await transcriptionModelsApi.create(value);
    emit("saved");
    close();
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : "Failed to save transcription model.";
  }
}
function close() {
  cancelTest();
  emit("close");
}

function syncCapabilities() {
  if (contextPromptsLocked.value) {
    form.supports_prompted = false;
  }
  if (timecodesLocked.value) form.supports_timecodes = false;
}

watch(
  () => form.protocol,
  () => {
    cancelTest();
    if (!loadingForm.value) Object.assign(form, protocolDefaults[form.protocol]);
  },
  { flush: "sync" },
);

watch(
  [
    () => form.model_name,
    () => form.api_profile,
    () => form.request_encoding,
    () => form.protocol,
  ],
  syncCapabilities,
  {
    flush: "sync",
    immediate: true,
  },
);

watch(
  [
    () => form.provider,
    () => form.endpoint,
    () => form.model_name,
    () => form.api_profile,
    () => form.request_encoding,
    () => props.editModel?.model_id,
  ],
  () => {
    if (isBusy.value) cancelTest();
  },
  { flush: "sync" },
);

watch(
  () => props.isOpen,
  (open) => {
    if (!open) {
      cancelTest();
      return;
    }
    reset();
    const model = props.editModel;
    loadingForm.value = true;
    Object.assign(
      form,
      model
        ? {
            display_name: model.display_name,
            protocol: model.protocol,
            provider: model.provider,
            endpoint: model.endpoint,
            api_key: "",
            model_name: model.model_name,
            api_profile: model.api_profile ?? "auto",
            request_encoding: model.request_encoding ?? "auto",
            supports_prompted: model.supports_prompted,
            supports_timecodes: model.supports_timecodes,
          }
        : {
            api_key: "",
            ...protocolDefaults.openai,
          },
    );
    loadingForm.value = false;
    syncCapabilities();
  },
  { immediate: true },
);
onBeforeUnmount(cancelTest);
</script>
