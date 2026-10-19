<!-- Add / edit dialog for a custom model. Renders as its own Modal (a stacked
     PrimeVue Dialog) layered on top of ModelsModal's list, so it has a proper
     title + close button and the list stays visible/unchanged underneath
     rather than being swapped out. Owns the whole form + test/save lifecycle;
     emits `saved` (parent reloads the list) and `close`. `editModel` is the
     custom model being edited, or null when adding. -->
<template>
  <Modal
    :is-open="isOpen"
    :title="
      editProvider ? t('editProviderCredentials') : editModel ? t('editModel') : t('modelsAndKeys')
    "
    class-name="modal-wide"
    @close="emit('close')"
  >
    <div class="model-form">
      <div v-if="error" class="models-error">
        {{ error }}
        <button class="models-error-dismiss" @click="error = null">×</button>
      </div>

      <div v-if="!editModel && !editProvider" class="form-group">
        <div class="endpoint-toggle" role="group" :aria-label="t('modelsAndKeys')">
          <button
            type="button"
            :aria-pressed="wholeKey"
            :class="`toggle-btn ${wholeKey ? 'selected' : ''}`"
            @click="setWholeKey(true)"
          >
            {{ t("addModelAll") }}
          </button>
          <button
            type="button"
            :aria-pressed="!wholeKey"
            :class="`toggle-btn ${!wholeKey ? 'selected' : ''}`"
            @click="setWholeKey(false)"
          >
            {{ t("addModelSingle") }}
          </button>
        </div>
      </div>

      <!-- Provider Selection -->
      <div class="form-group">
        <label>{{ t("providerApiFormat") }}</label>
        <div class="provider-buttons" role="group" :aria-label="t('providerApiFormat')">
          <button
            v-for="p in wholeKey ? connectionProviderTypes : providerTypes"
            :key="p"
            type="button"
            :aria-pressed="form.provider_type === p"
            :class="`provider-btn ${form.provider_type === p ? 'selected' : ''}`"
            @click="handleProviderChange(p)"
          >
            {{ PROVIDER_TAB_LABELS[p] }}
          </button>
        </div>
      </div>

      <!-- Endpoint Selection -->
      <div class="form-group">
        <label for="model-endpoint">{{ t("endpoint") }}</label>
        <InputGroup ref="endpointGroup" tabindex="-1">
          <InputText
            id="model-endpoint"
            v-model="form.endpoint"
            type="text"
            inputmode="url"
            :placeholder="t('endpoint')"
            :dt="inputFieldDt"
            autocomplete="off"
            autocapitalize="none"
            autocorrect="off"
            :spellcheck="false"
            @keydown.down.prevent="endpointMenu?.show($event, endpointGroup?.$el)"
          />
          <Button
            icon="pi pi-chevron-down"
            severity="secondary"
            outlined
            :aria-label="t('endpoint')"
            aria-haspopup="menu"
            aria-controls="model-endpoint-presets"
            :aria-expanded="endpointMenuOpen"
            @click="endpointMenu?.toggle($event, endpointGroup?.$el)"
          />
        </InputGroup>
        <Menu
          id="model-endpoint-presets"
          ref="endpointMenu"
          :model="endpointMenuItems"
          popup
          :aria-label="t('endpoint')"
          :pt="{
            root: { style: { maxWidth: 'calc(100vw - 2rem)' } },
            list: { style: { maxHeight: 'min(18rem, 45dvh)', overflowY: 'auto' } },
          }"
          @show="endpointMenuOpen = true"
          @hide="endpointMenuOpen = false"
          @keydown.esc.stop
        >
          <template #item="{ item, props: itemProps }">
            <a v-bind="itemProps.action">
              <span class="endpoint-preset">
                <span>{{ item.label }}</span>
                <small>{{ item.endpoint }}</small>
              </span>
            </a>
          </template>
        </Menu>
      </div>

      <template v-if="wholeKey">
        <div class="form-group">
          <label for="provider-connection-key">{{ t("apiKey") }}</label>
          <InputText
            id="provider-connection-key"
            v-model="form.api_key"
            type="password"
            :placeholder="keyRequired ? t('enterApiKey') : t('providerKeyBlankRetains')"
            fluid
            :dt="inputFieldDt"
            autocomplete="off"
            autocapitalize="none"
            autocorrect="off"
            :spellcheck="false"
            data-1p-ignore
            data-lpignore="true"
            data-bwignore="true"
            data-form-type="other"
          />
          <div v-if="editProvider && originChanged" class="form-hint">
            {{ t("providerKeyRequiredNewHost") }}
          </div>

        </div>
        <div class="form-group">
          <label for="provider-connection-name">{{ t("accountLabel") }}</label>
          <InputText
            id="provider-connection-name"
            v-model="form.display_name"
            :placeholder="t('accountLabelPlaceholder')"
            fluid
            :dt="inputFieldDt"
            autocomplete="off"
          />
        </div>
        <div
          v-if="shownTestResult"
          :class="`test-result ${shownTestResult.success ? 'success' : 'error'}`"
        >
          {{ shownTestResult.message }}
        </div>
        <div class="form-actions">
          <Button type="button" severity="secondary" :label="t('cancel')" @click="emit('close')" />
          <Button
            type="button"
            severity="secondary"
            :label="testing ? t('checkingModels') : t('checkModels')"
            :disabled="testing || saving || !providerReady"
            @click="testProvider"
          />
          <Button
            type="button"
            :label="
              saving
                ? editProvider
                  ? t('savingProvider')
                  : t('connectingProvider')
                : editProvider
                  ? t('save')
                  : t('connectProvider')
            "
            :disabled="saving || !providerReady"
            @click="saveProvider"
          />
        </div>
      </template>

      <template v-else>
        <!-- Model Name with autocomplete suggestions -->
        <div class="form-group">
          <label>{{ t("model") }}</label>
          <InputText
            :model-value="form.model_name"
            placeholder="Model name (e.g., claude-sonnet-4-6)"
            fluid
            :dt="inputFieldDt"
            :list="`model-name-suggestions-${form.provider_type}`"
            autocomplete="off"
            @update:model-value="onModelNameInput($event ?? '')"
          />
          <datalist :id="`model-name-suggestions-${form.provider_type}`">
            <option
              v-for="preset in DEFAULT_MODELS[form.provider_type]"
              :key="preset.model_name"
              :value="preset.model_name"
            >
              {{ preset.name }}
            </option>
          </datalist>
        </div>

        <!-- Display Name -->
        <div class="form-group">
          <label>{{ t("displayName") }}</label>
          <InputText
            v-model="form.display_name"
            :placeholder="t('nameShownInSelector')"
            fluid
            :dt="inputFieldDt"
          />
        </div>

        <!-- API Key -->
        <div class="form-group">
          <label>{{ t("apiKey") }}</label>
          <InputText
            v-model="form.api_key"
            type="password"
            :placeholder="t('enterApiKey')"
            fluid
            :dt="inputFieldDt"
            autocomplete="off"
            autocapitalize="none"
            autocorrect="off"
            :spellcheck="false"
            data-1p-ignore
            data-lpignore="true"
            data-bwignore="true"
            data-form-type="other"
          />
        </div>

        <!-- Maximum generated response tokens for custom models. -->
        <div class="form-group">
          <label for="custom-model-max-output-tokens">{{ t("maxOutputTokens") }}</label>
          <InputText
            id="custom-model-max-output-tokens"
            type="number"
            min="1"
            step="1"
            :model-value="form.max_tokens ? String(form.max_tokens) : ''"
            :placeholder="t('maxOutputTokensPlaceholder')"
            fluid
            :dt="inputFieldDt"
            @update:model-value="form.max_tokens = parseInt($event ?? '', 10) || 0"
          />
          <div class="form-hint">{{ t("maxOutputTokensHelp") }}</div>
        </div>

        <!-- Image input support -->
        <div class="form-group">
          <label>{{ t("imageSupport") }}</label>
          <Select
            v-model="form.image_support"
            :options="imageSupportOptions"
            option-label="label"
            option-value="value"
            fluid
            :dt="selectFieldDt"
          />
          <div class="form-hint">{{ t("imageSupportHelp") }}</div>
          <div v-if="editingResolvedAuto" class="form-hint">
            <code>auto({{ editingResolvedAuto.endpoint }}, {{ editingResolvedAuto.model }})</code>
            {{ t("imageSupportAutoResolved") }}
            {{ editingResolvedAuto.supported ? t("imageSupportYes") : t("imageSupportNo") }}
          </div>
        </div>

        <!-- Legacy provider default for OpenAI Responses models -->
        <div v-if="form.provider_type === 'openai-responses'" class="form-group">
          <label>{{ t("reasoningEffort") }}</label>
          <InputText
            v-model="form.reasoning_effort"
            :placeholder="t('reasoningEffortPlaceholder')"
            fluid
            :dt="inputFieldDt"
            list="reasoning-effort-suggestions"
            autocomplete="off"
          />
          <datalist id="reasoning-effort-suggestions">
            <option
              v-for="suggestion in REASONING_EFFORT_SUGGESTIONS"
              :key="suggestion"
              :value="suggestion"
            />
          </datalist>
          <div class="form-hint">{{ t("reasoningEffortHint") }}</div>
        </div>

        <!-- Reasoning capability and generic level mapping -->
        <div class="form-group">
          <label>{{ t("supportsReasoning") }}</label>
          <Select
            v-model="form.reasoning_support"
            :options="reasoningSupportOptions"
            option-label="label"
            option-value="value"
            fluid
            :dt="selectFieldDt"
          />
          <div class="form-hint">
            {{ t("reasoningSupportHelp") }}
          </div>
        </div>
        <div
          v-if="form.provider_type === 'openai' || form.provider_type === 'openai-responses'"
          class="form-group"
        >
          <label>{{ t("reasoningReplay") }}</label>
          <Select
            v-model="form.reasoning_replay"
            :options="reasoningReplayOptions"
            option-label="label"
            option-value="value"
            fluid
            :dt="selectFieldDt"
          />
          <div class="form-hint">{{ t("reasoningReplayHelp") }}</div>
          <div v-if="editingReasoningReplayAuto" class="form-hint">
            {{ t("reasoningReplayAutoResolved") }}
            <code>{{ editingReasoningReplayAuto }}</code>
          </div>
        </div>
        <div v-if="form.reasoning_support !== 'no'" class="form-group">
          <label>{{ t("reasoningLevelMapping") }}</label>
          <div class="reasoning-map-grid">
            <label v-for="level in REASONING_LEVELS" :key="level" class="reasoning-map-row">
              <span>{{ level }}</span>
              <span class="reasoning-map-arrow" aria-hidden="true">→</span>
              <InputText
                v-model="form.reasoning_map[level]"
                class="reasoning-map-input"
                fluid
                :dt="inputFieldDt"
                :list="`reasoning-map-${level}`"
                :placeholder="t('reasoningMappingUnsupported')"
              />
              <datalist :id="`reasoning-map-${level}`">
                <option v-for="target in REASONING_LEVELS" :key="target" :value="target" />
                <option value="none" />
              </datalist>
            </label>
          </div>
          <div class="form-hint">
            {{ t("reasoningMappingHelp") }}
          </div>
        </div>

        <!-- Test Result -->
        <div
          v-if="shownTestResult"
          :class="`test-result ${shownTestResult.success ? 'success' : 'error'}`"
        >
          {{ shownTestResult.success ? "✓" : "✗" }} {{ shownTestResult.message }}
        </div>

        <!-- Form Actions -->
        <div class="form-actions">
          <Button type="button" severity="secondary" :label="t('cancel')" @click="emit('close')" />
          <Button
            type="button"
            severity="secondary"
            :label="testing ? t('testingButton') : t('testButton')"
            :disabled="testing || saving || (!form.api_key && !editModel) || !form.model_name"
            :title="
              !form.model_name
                ? 'Enter model name to test'
                : !form.api_key && !editModel
                  ? 'Enter API key to test'
                  : ''
            "
            @click="handleTest"
          />
          <Button
            type="button"
            :label="saving ? t('savingProvider') : editModel ? t('save') : t('addSingleModelSubmit')"
            :disabled="saving || !form.display_name || !form.api_key || !form.model_name"
            @click="handleSave"
          />
        </div>
      </template>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch, type ComponentPublicInstance } from "vue";
import Button from "primevue/button";

import InputText from "primevue/inputtext";
import InputGroup from "primevue/inputgroup";
import Menu from "primevue/menu";
import Select from "primevue/select";
import { inputFieldDt, selectFieldDt } from "./configFieldDt";
import Modal from "./Modal.vue";
import { useI18n } from "../composables/i18n";
import {
  api,
  customModelsApi,
  type ModelProviderConnection,
  type CustomModel,
  type CreateCustomModelRequest,
  type TestCustomModelRequest,
} from "../../services/api";
import {
  CONNECTION_DEFAULT_ENDPOINTS,
  DEFAULT_ENDPOINTS,
  ENDPOINT_PRESETS,
  DEFAULT_MODELS,
  DEFAULT_REASONING_MAP,
  PROVIDER_TAB_LABELS,
  REASONING_EFFORT_SUGGESTIONS,
  REASONING_LEVELS,
  emptyForm,
  connectionProviderTypes,
  providerTypes,
  type FormData,
  type ProviderType,
} from "./customModelConstants";

const props = defineProps<{
  isOpen: boolean;
  editModel: CustomModel | null;
  editProvider: ModelProviderConnection | null;
}>();
const emit = defineEmits<{
  (e: "close"): void;
  (e: "saved"): void;
  (e: "providerSaved", provider: ModelProviderConnection): void;
}>();

const { t } = useI18n();

const imageSupportOptions = computed(() => [
  { label: t("imageSupportAuto"), value: "auto" },
  { label: t("imageSupportYes"), value: "yes" },
  { label: t("imageSupportNo"), value: "no" },
]);
const reasoningSupportOptions = computed(() => [
  { label: t("reasoningSupportAuto"), value: "auto" },
  { label: t("reasoningSupportYes"), value: "yes" },
  { label: t("reasoningSupportNo"), value: "no" },
]);
const reasoningReplayOptions = computed(() => [
  { label: t("reasoningReplayAuto"), value: "auto" },
  { label: t("reasoningReplayNone"), value: "none" },
  { label: "reasoning_content", value: "reasoning_content" },
]);

const form = reactive<FormData>({ ...emptyForm });

function presetsFor(provider: ProviderType, allModels: boolean) {
  return ENDPOINT_PRESETS[provider].map((preset) => ({
    ...preset,
    url: provider === "anthropic" && !allModels ? preset.url + "/messages" : preset.url,
  }));
}

const endpointPresets = computed(() => presetsFor(form.provider_type, wholeKey.value));
const endpointGroup = ref<ComponentPublicInstance | null>(null);
const endpointMenu = ref<InstanceType<typeof Menu> | null>(null);
const endpointMenuOpen = ref(false);
const endpointMenuItems = computed(() =>
  endpointPresets.value.map((preset) => ({
    label: preset.name,
    endpoint: preset.url,
    command: () => {
      form.endpoint = preset.url;
    },
  })),
);
const error = ref<string | null>(null);
const wholeKey = ref(true);
const saving = ref(false);
let openGeneration = 0;
const testing = ref(false);
// A Check result belongs to the exact inputs (and request) that produced it.
// `signature` captures mode, edit identity, format, endpoint, key and the
// other test inputs; any edit bumps `checkSeq`, which orphans a pending
// request, and clears the stale result.
const testResult = ref<{ signature: string; success: boolean; message: string } | null>(null);
let checkSeq = 0;

function originOf(url: string): string | null {
  try {
    return new URL(url.trim()).origin;
  } catch {
    return null;
  }
}

// The backend refuses to send a saved key to a different origin, so an edit
// that moves the endpoint to another scheme/host/port needs the key re-entered.
const originChanged = computed(() => {
  const saved = props.editProvider;
  if (!saved) return false;
  const before = originOf(saved.endpoint);
  const after = originOf(form.endpoint);
  if (before === null || after === null) return saved.endpoint.trim() !== form.endpoint.trim();
  return before !== after;
});
const keyRequired = computed(() => !props.editProvider || originChanged.value);
const providerReady = computed(
  () => !!form.endpoint.trim() && (!keyRequired.value || !!form.api_key.trim()),
);

const inputSignature = computed(() =>
  wholeKey.value
    ? JSON.stringify([
        "provider",
        props.editProvider?.provider_id ?? null,
        form.provider_type,
        form.endpoint.trim(),
        form.api_key,
      ])
    : JSON.stringify([
        "model",
        props.editModel?.model_id ?? null,
        form.provider_type,
        form.endpoint,
        form.api_key,
        form.model_name,
        form.max_tokens,
        form.reasoning_effort,
        form.reasoning_replay,
        form.reasoning_support,
        serializeReasoningMap(),
      ]),
);
const shownTestResult = computed(() =>
  testResult.value && testResult.value.signature === inputSignature.value ? testResult.value : null,
);

function invalidateCheck() {
  checkSeq++;
  testing.value = false;
  testResult.value = null;
}

function defaultEndpoint(provider: ProviderType): string {
  return wholeKey.value && provider !== "gemini"
    ? CONNECTION_DEFAULT_ENDPOINTS[provider]
    : DEFAULT_ENDPOINTS[provider];
}

function resetForm() {
  Object.assign(form, emptyForm, { reasoning_map: { ...DEFAULT_REASONING_MAP } });
  form.endpoint = defaultEndpoint(form.provider_type);
}

function setWholeKey(value: boolean) {
  const selectedPreset = endpointPresets.value.find((preset) => preset.url === form.endpoint);
  wholeKey.value = value;
  error.value = null;
  if (value && form.provider_type === "gemini") {
    form.provider_type = "anthropic";
    form.endpoint = defaultEndpoint(form.provider_type);
  } else if (selectedPreset) {
    form.endpoint = endpointPresets.value.find((preset) => preset.name === selectedPreset.name)!.url;
  }
}

function serializeReasoningMap(): string {
  return JSON.stringify(
    Object.fromEntries(
      REASONING_LEVELS.flatMap((level) => {
        const target = form.reasoning_map[level];
        return target ? [[level, target]] : [];
      }),
    ),
  );
}

function parseReasoningMap(raw: string) {
  const result = { ...DEFAULT_REASONING_MAP };
  if (!raw) return result;
  try {
    const parsed = JSON.parse(raw) as Record<string, string>;
    for (const level of REASONING_LEVELS) result[level] = parsed[level] || "";
  } catch {
    // Invalid stored mappings are rejected by the server; keep safe defaults.
  }
  return result;
}

// When editing a model whose image support is Auto, surface what auto() would
// resolve to (from the model's stored endpoint/model + server verdict).
const editingResolvedAuto = computed(() => {
  if (form.image_support !== "auto" || !props.editModel) return null;
  return {
    endpoint: props.editModel.endpoint || "\u2014",
    model: props.editModel.model_name || "\u2014",
    supported: props.editModel.supports_images ?? true,
  };
});

const editingReasoningReplayAuto = computed(() => {
  const saved = props.editModel;
  if (
    form.reasoning_replay !== "auto" ||
    !saved ||
    saved.reasoning_replay !== "auto" ||
    form.endpoint !== saved.endpoint ||
    form.model_name !== saved.model_name
  )
    return null;
  return saved.resolved_reasoning_replay || t("reasoningReplayNone");
});

// Populate the form from editModel (or reset to blank for add) each time the
// dialog opens, and clear any stale error/test state.
watch(
  () => [props.isOpen, props.editModel, props.editProvider] as const,
  ([open]) => {
    openGeneration++;
    saving.value = false;
    invalidateCheck();
    if (!open) return;
    error.value = null;
    const m = props.editModel;
    const provider = props.editProvider;
    wholeKey.value = !m;
    if (provider) {
      resetForm();
      Object.assign(form, {
        display_name: provider.display_name,
        provider_type: provider.provider_type,
        endpoint: provider.endpoint,

        api_key: "",
      });
    } else if (m) {
      Object.assign(form, {
        display_name: m.display_name,
        provider_type: m.provider_type,
        endpoint: m.endpoint,

        api_key: m.api_key,
        model_name: m.model_name,
        max_tokens: m.max_tokens,
        reasoning_effort: m.reasoning_effort || "",
        reasoning_replay: m.reasoning_replay || "auto",
        reasoning_support: m.reasoning_support || "auto",
        reasoning_map: parseReasoningMap(m.reasoning_map),
        image_support: m.image_support ?? "auto",
      });
    } else {
      resetForm();
    }
  },
  { immediate: true },
);

// Any change to what a Check would send discards its result and any pending
// request. Registered after the open watcher so loading a form is not an edit.
watch(inputSignature, invalidateCheck, { flush: "sync" });

function handleProviderChange(provider: ProviderType) {
  const wasPreset = endpointPresets.value.some((preset) => preset.url === form.endpoint);
  form.provider_type = provider;
  if (wasPreset && !endpointPresets.value.some((preset) => preset.url === form.endpoint)) {
    form.endpoint = defaultEndpoint(provider);
  }
}

function providerInput() {
  return {
    display_name: form.display_name.trim(),
    provider_type: form.provider_type as Exclude<ProviderType, "gemini">,
    endpoint: form.endpoint.trim(),
    // Empty on edit tells the server to use its stored key.
    api_key: form.api_key,
  };
}

async function testProvider() {
  if (testing.value || form.provider_type === "gemini" || !providerReady.value) return;
  const seq = ++checkSeq;
  const signature = inputSignature.value;
  testing.value = true;
  testResult.value = null;
  try {
    const { chat, key_checked } = await api.testModelProvider(
      providerInput(),
      props.editProvider?.provider_id,
    );
    if (seq !== checkSeq) return;
    testResult.value = {
      signature,
      success: chat.length > 0,
      message:
        chat.length === 0
          ? t("providerNoModels")
          : t("providerChatModelCount").replace("{count}", String(chat.length)) +
            `: ${chat.slice(0, 5).join(", ")}${chat.length > 5 ? ", …" : ""}` +
            (key_checked ? "" : ` (${t("providerKeyNotChecked")})`),
    };
  } catch (err) {
    if (seq === checkSeq)
      testResult.value = {
        signature,
        success: false,
        message: err instanceof Error ? err.message : String(err),
      };
  } finally {
    if (seq === checkSeq) testing.value = false;
  }
}

async function saveProvider() {
  if (saving.value || form.provider_type === "gemini" || !providerReady.value) return;
  const generation = openGeneration;
  saving.value = true;
  error.value = null;
  const input = providerInput();
  let saved: ModelProviderConnection | null = null;
  try {
    saved = props.editProvider
      ? await api.updateModelProvider(props.editProvider.provider_id, input)
      : await api.createModelProvider(input);
  } catch (err) {
    if (generation === openGeneration)
      error.value = err instanceof Error ? err.message : String(err);
  } finally {
    if (generation === openGeneration) saving.value = false;
  }
  if (!saved) return;
  // The server mutation happened: the catalog owner must hear about it even if
  // this form was closed or reopened meanwhile. Only dialog-local effects are
  // tied to the originating form.
  emit("providerSaved", saved);
  if (generation === openGeneration) emit("close");
}

function onModelNameInput(v: string) {
  const preset = DEFAULT_MODELS[form.provider_type].find((p) => p.model_name === v);
  form.model_name = v;
  if (preset && !form.display_name) form.display_name = preset.name;
}

async function handleTest() {
  const signature = inputSignature.value;
  if (!form.model_name) {
    testResult.value = { signature, success: false, message: t("modelNameRequired") };
    return;
  }
  if (!form.api_key && !props.editModel) {
    testResult.value = { signature, success: false, message: t("apiKeyRequired") };
    return;
  }
  const seq = ++checkSeq;
  testing.value = true;
  testResult.value = null;
  try {
    const request: TestCustomModelRequest = {
      model_id: props.editModel?.model_id || undefined,
      provider_type: form.provider_type,
      endpoint: form.endpoint.trim(),
      api_key: form.api_key,
      model_name: form.model_name,
      max_tokens: form.max_tokens,
      reasoning_effort: form.reasoning_effort,
      reasoning_replay: form.reasoning_replay,
      reasoning_support: form.reasoning_support,
      reasoning_map: serializeReasoningMap(),
    };
    const result = await customModelsApi.testCustomModel(request);
    if (seq === checkSeq) testResult.value = { signature, ...result };
  } catch (err) {
    if (seq === checkSeq)
      testResult.value = {
        signature,
        success: false,
        message: err instanceof Error ? err.message : "Test failed",
      };
  } finally {
    if (seq === checkSeq) testing.value = false;
  }
}

async function handleSave() {
  if (saving.value) return;
  if (!form.display_name || !form.api_key || !form.model_name) {
    error.value = "Display name, API key, and model name are required";
    return;
  }
  const generation = openGeneration;
  saving.value = true;
  error.value = null;
  let succeeded = false;
  try {
    const request: CreateCustomModelRequest = {
      display_name: form.display_name,
      provider_type: form.provider_type,
      endpoint: form.endpoint.trim(),
      api_key: form.api_key,
      model_name: form.model_name,
      max_tokens: form.max_tokens,
      reasoning_effort: form.reasoning_effort,
      reasoning_replay: form.reasoning_replay,
      reasoning_support: form.reasoning_support,
      reasoning_map: serializeReasoningMap(),
      image_support: form.image_support,
    };
    if (props.editModel) {
      await customModelsApi.updateCustomModel(props.editModel.model_id, request);
    } else {
      await customModelsApi.createCustomModel(request);
    }
    succeeded = true;
  } catch (err) {
    if (generation === openGeneration)
      error.value = err instanceof Error ? err.message : "Failed to save model";
  } finally {
    if (generation === openGeneration) saving.value = false;
  }
  if (!succeeded) return;
  // Same rule as provider saves: the owner always learns the catalog changed;
  // only this form's own close is tied to its generation.
  emit("saved");
  if (generation === openGeneration) emit("close");
}
</script>
