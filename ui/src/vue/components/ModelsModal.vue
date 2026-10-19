<!-- Manage Models: lists built-in + custom models and owns duplicate/delete +
     refresh. The list is a compact PrimeVue DataTable (size="small" +
     modelsTableDt tokens) grouped by source via rowGroupMode="subheader": in
     the default single-gateway install this collapses what used to be two
     full columns of the same repeated hostname/URL into one quiet group
     header line. Endpoints render per-row only when they differ from the
     group's common endpoint. Only custom rows carry a `model` and thus the
     edit/duplicate/delete actions. Adding/editing opens ModelFormModal — a
     separate stacked dialog layered on top of this one. Uses Modal.vue
     (#title-right slot), useI18n, customModelsApi; shared form constants live
     in customModelConstants.ts. -->
<template>
  <Modal
    :is-open="isOpen"
    :title="t('manageModels')"
    class-name="modal-xwide"
    @close="emit('close')"
  >
    <template #title-right>
      <div class="models-header-actions">
        <Button
          :label="refreshing ? t('refreshingModels') : t('refreshModels')"
          severity="secondary"
          size="small"
          :disabled="refreshing || loading"
          @click="handleRefreshModels"
        />
        <Button size="small" @click="handleAddNew">+ {{ t("addModel") }}</Button>
      </div>
    </template>

    <div class="models-modal" :class="{ 'models-modal-list': showList }">
      <div v-if="error || loadError" class="models-error">
        {{ error || loadError }}
        <button
          class="models-error-dismiss"
          @click="
            error = null;
            loadError = null;
          "
        >
          ×
        </button>
      </div>

      <div v-if="loading" class="models-loading">
        <div class="spinner"></div>
        <span>{{ t("loadingModels") }}</span>
      </div>

      <!-- Empty state -->
      <div v-else-if="!hasAnyEntries" class="models-empty">
        <p>{{ t("noModelsConfigured") }}</p>
        <p class="models-empty-hint">{{ t("noModelsHint") }}</p>
      </div>

      <!-- Model List -->
      <DataTable
        v-if="showList"
        :value="tableRows"
        data-key="key"
        size="small"
        scrollable
        scroll-height="flex"
        :dt="modelsTableDt"
        class="models-datatable"
        row-group-mode="subheader"
        group-rows-by="groupKey"
        :pt="{ rowGroupHeaderCell: { colspan: 5 } }"
      >
        <template #groupheader="{ data }">
          <div v-if="data.provider" class="models-provider-group">
            <div class="models-provider-line">
              <span class="models-group-name">{{ data.groupLabel }}</span>
              <span
                v-if="accountSuffix(data.provider)"
                class="models-group-endpoint"
                :title="t('accountLabel')"
                >{{ accountSuffix(data.provider) }}</span
              >
              <span class="models-group-count">({{ groupCounts[data.groupKey] ?? 0 }})</span>
              <span class="models-group-endpoint">{{ data.provider.endpoint }}</span>
              <span class="models-provider-actions">
                <Button
                  class="btn-icon"
                  text
                  severity="secondary"
                  v-tooltip.top="t('refresh')"
                  :aria-label="t('refresh')"
                  :disabled="refreshingProviderIds.has(data.provider.provider_id)"
                  @click="refreshProvider(data.provider)"
                >
                  <svg
                    :class="{ spinning: refreshingProviderIds.has(data.provider.provider_id) }"
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                    width="16"
                    height="16"
                  >
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      :stroke-width="2"
                      d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
                    />
                  </svg>
                </Button>
                <Button
                  class="btn-icon"
                  text
                  severity="secondary"
                  v-tooltip.top="t('editProviderCredentials')"
                  :aria-label="t('editProviderCredentials')"
                  @click="openProviderForm(data.provider)"
                >
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      :stroke-width="2"
                      d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"
                    />
                  </svg>
                </Button>
                <Button
                  class="btn-icon btn-danger"
                  text
                  severity="danger"
                  v-tooltip.top="t('removeProvider')"
                  :aria-label="t('removeProvider')"
                  @click="openRemoveProvider(data.provider)"
                >
                  <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      :stroke-width="2"
                      d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                    />
                  </svg>
                </Button>
              </span>
            </div>
            <div v-if="data.provider.error" class="models-provider-error" role="status">
              {{ data.provider.error }}
              <template v-if="data.provider.model_count > 0">
                {{ t("providerUsingSavedList") }}
              </template>
              <template v-if="lastSuccessText(data.provider)">
                ({{ t("providerLastSuccess") }}: {{ lastSuccessText(data.provider) }})
              </template>
            </div>
          </div>
          <template v-else>
            <span class="models-group-name">{{ data.groupLabel }}</span>
            <span class="models-group-count">({{ groupCounts[data.groupKey] }})</span>
            <span v-if="data.groupEndpoint" class="models-group-endpoint">{{
              data.groupEndpoint
            }}</span>
          </template>
        </template>
        <Column :header="t('columnName')" field="name">
          <template #body="{ data }">
            <span v-if="data.placeholder" class="models-cell-muted">{{ data.note }}</span>
            <template v-else>
              <span class="models-cell-name">{{ data.name }}</span>
            </template>
          </template>
        </Column>
        <Column :header="t('columnModelId')" field="modelId">
          <template #body="{ data }">
            <span class="models-cell-mono" :title="data.modelId">{{ data.wireId }}</span>
            <div v-if="data.endpoint" class="models-cell-endpoint" :title="data.endpoint">
              {{ data.endpoint }}
            </div>
          </template>
        </Column>
        <Column :header="t('columnProvider')" field="apiShape">
          <template #body="{ data }">
            <span :class="{ 'models-cell-muted': !data.apiShape }">{{
              data.placeholder ? "" : data.apiShape || "—"
            }}</span>
          </template>
        </Column>
        <Column :header="t('columnImages')" field="supportsImages" class="models-col-images">
          <template #body="{ data }">
            <span
              :class="
                data.supportsImages === null
                  ? 'models-cell-muted'
                  : data.supportsImages
                    ? 'models-table-image-yes'
                    : 'models-table-image-no'
              "
              role="img"
              :title="data.imageTitle"
              :aria-label="data.imageTitle"
              >{{
                data.placeholder
                  ? ""
                  : data.supportsImages === null
                    ? "?"
                    : data.supportsImages
                      ? "✓"
                      : "✕"
              }}<span v-if="data.imageAuto" class="models-table-image-auto-tag">{{
                t("imageSupportAutoShort")
              }}</span></span
            >
          </template>
        </Column>
        <Column class="models-col-actions">
          <template #header>
            <span class="sr-only">{{ t("columnActions") }}</span>
          </template>
          <template #body="{ data }">
            <div v-if="data.model" class="models-cell-actions">
              <Button
                class="btn-icon"
                text
                severity="secondary"
                v-tooltip.top="t('duplicate')"
                :aria-label="t('duplicate')"
                @click="handleDuplicate(data.model)"
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    :stroke-width="2"
                    d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"
                  />
                </svg>
              </Button>
              <Button
                class="btn-icon"
                text
                severity="secondary"
                v-tooltip.top="t('editModel')"
                :aria-label="t('editModel')"
                @click="handleEdit(data.model)"
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    :stroke-width="2"
                    d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"
                  />
                </svg>
              </Button>
              <Button
                class="btn-icon btn-danger"
                text
                severity="danger"
                v-tooltip.top="t('delete_')"
                :aria-label="t('delete_')"
                @click="handleDelete(data.model.model_id)"
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" width="16" height="16">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    :stroke-width="2"
                    d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                  />
                </svg>
              </Button>
            </div>
          </template>
        </Column>
      </DataTable>
    </div>
  </Modal>

  <!-- Stacked add/edit dialog, layered on top of the list. -->
  <ModelFormModal
    :is-open="formOpen"
    :edit-model="editModel"
    :edit-provider="editProvider"
    @saved="handleFormSaved"
    @provider-saved="handleProviderSaved"
    @close="formOpen = false"
  />
  <Modal
    :is-open="!!providerToRemove"
    :title="t('removeProviderTitle')"
    class-name="modal-wide"
    @close="closeRemoveProvider"
  >
    <form class="model-form" @submit.prevent="confirmRemoveProvider">
      <div v-if="removeError" class="models-error" role="alert">{{ removeError }}</div>
      <p class="models-info">
        {{
          t("removeProviderConfirmation").replace("{name}", providerToRemove?.display_name ?? "")
        }}
      </p>
      <div class="form-actions">
        <Button
          type="button"
          severity="secondary"
          :label="t('cancel')"
          :disabled="removingProvider"
          @click="closeRemoveProvider"
        />
        <Button
          type="submit"
          severity="danger"
          :label="t('removeProvider')"
          :disabled="removingProvider || !providerToRemove"
        />
      </div>
    </form>
  </Modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import DataTable from "primevue/datatable";
import Column from "primevue/column";
import Button from "primevue/button";
import Modal from "./Modal.vue";
import ModelFormModal from "./ModelFormModal.vue";
import { modelsTableDt } from "./modelsTableDt";
import { prettyModelLabels } from "../../utils/modelNames";
import { accountSuffixes } from "../../utils/providerAccounts";
import {
  API_TYPE_LABELS,
  CONNECTION_PROVIDER_LABELS,
  PROVIDER_LABELS,
} from "./customModelConstants";
import { useI18n } from "../composables/i18n";
import { latestGate } from "../../services/latestGate";
import {
  api,
  ApiError,
  customModelsApi,
  type AvailableModel,
  type CustomModel,
  type ModelProviderConnection,
} from "../../services/api";

// refreshTrigger changes whenever the server reports a catalog or provider
// status change; an open modal reloads its lists in place.
const props = defineProps<{ isOpen: boolean; refreshTrigger?: number }>();
const emit = defineEmits<{ (e: "close"): void; (e: "modelsChanged"): void }>();

const { t } = useI18n();

const models = ref<CustomModel[]>([]);
const loading = ref(true);
const refreshing = ref(false);
// Errors from user actions (`error`) and from reloading the lists (`loadError`)
// are kept apart so a late reload can neither clear nor overwrite an action's
// error.
const error = ref<string | null>(null);
const loadError = ref<string | null>(null);
const builtInModels = ref<AvailableModel[]>([]);
const providerConnections = ref<ModelProviderConnection[]>([]);
const refreshingProviderIds = ref(new Set<string>());

// Stacked add/edit dialog state. `editModel` is the custom model being edited,
// or null when adding a new one.
const formOpen = ref(false);
const editModel = ref<CustomModel | null>(null);
const editProvider = ref<ModelProviderConnection | null>(null);
const providerToRemove = ref<ModelProviderConnection | null>(null);
const removingProvider = ref(false);
// Shown inside the confirm dialog, which sits above the list's error banner.
const removeError = ref<string | null>(null);

function openRemoveProvider(provider: ModelProviderConnection) {
  removeError.value = null;
  providerToRemove.value = provider;
}
function closeRemoveProvider() {
  removeError.value = null;
  providerToRemove.value = null;
}

const builtInModelsFiltered = computed(() =>
  builtInModels.value.filter((m) => m.id !== "predictable"),
);

// True when the model-list DataTable (not the empty/loading views) is showing.
// The list fills the modal edge-to-edge, so we drop the wrapper padding in that
// state (see .models-modal-list).
// Saved provider connections count even with zero (or failed) models: their
// error text and refresh/edit/remove controls are the recovery path.
const hasAnyEntries = computed(
  () =>
    builtInModels.value.length > 0 ||
    models.value.length > 0 ||
    providerConnections.value.length > 0,
);
const showList = computed(() => !loading.value && hasAnyEntries.value);

const savedAccountSuffixes = computed(() =>
  accountSuffixes(
    providerConnections.value.map((provider) => ({
      id: provider.provider_id,
      label: provider.display_name || CONNECTION_PROVIDER_LABELS[provider.provider_type],
    })),
  ),
);
function accountSuffix(provider: ModelProviderConnection): string {
  return savedAccountSuffixes.value.get(provider.provider_id) ?? "";
}

function lastSuccessText(provider: ModelProviderConnection): string {
  if (!provider.last_success) return "";
  const when = new Date(provider.last_success);
  return Number.isNaN(when.getTime()) ? "" : when.toLocaleString();
}

// Normalized rows so built-in + custom models render through one DataTable.
// `model` is only present for custom rows, which are the editable/deletable
// ones (built-ins have no actions). Rows are grouped by source (groupKey /
// groupLabel drive the subheader); a group's common endpoint renders once in
// the header, so per-row `endpoint` is only set when it differs (or for
// custom rows, whose endpoints are user-configured and always shown).
interface TableRow {
  key: string;
  groupKey: string;
  groupLabel: string;
  groupEndpoint: string;
  name: string;
  modelId: string;
  apiShape: string | null;
  endpoint: string;
  supportsImages: boolean | null;
  imageTitle: string;
  imageAuto: boolean;
  model: CustomModel | null;
  provider: ModelProviderConnection | null;
  placeholder: boolean;
  /** Placeholder text for a provider group with no rows. */
  note: string;
  wireId: string;
}

const tableRows = computed<TableRow[]>(() => {
  const labels = prettyModelLabels(builtInModelsFiltered.value);
  const connectionsById = new Map(
    providerConnections.value.map((provider) => [provider.provider_id, provider]),
  );
  const groups = new Map<string, { label: string; models: AvailableModel[] }>();
  for (const provider of providerConnections.value) {
    groups.set(`provider:${provider.provider_id}`, {
      label: provider.display_name || CONNECTION_PROVIDER_LABELS[provider.provider_type],
      models: [],
    });
  }
  for (const m of builtInModelsFiltered.value) {
    const src = m.source || "";
    const sourceId = m.source_id || "";
    const key = sourceId ? `provider:${sourceId}` : `builtin:${src}`;
    if (!groups.has(key)) {
      const provider = sourceId ? connectionsById.get(sourceId) : undefined;
      groups.set(key, { label: provider?.display_name || src, models: [] });
    }
    groups.get(key)!.models.push(m);
  }
  const rows: TableRow[] = [];
  for (const [groupKey, { label, models: group }] of groups) {
    // Endpoint shared by every model in the group renders once in the group
    // header; otherwise it stays per-row.
    const endpoints = new Set(group.map((m) => m.base_url || ""));
    const common = endpoints.size === 1 ? (group[0].base_url ?? "") : "";
    const provider = groupKey.startsWith("provider:")
      ? (connectionsById.get(groupKey.slice("provider:".length)) ?? null)
      : null;
    if (provider && group.length === 0) {
      rows.push({
        key: `${groupKey}:empty`,
        groupKey,
        groupLabel: label,
        groupEndpoint: "",
        name: "",
        modelId: "",
        apiShape: null,
        endpoint: "",
        supportsImages: null,
        imageTitle: "",
        imageAuto: false,
        model: null,
        provider,
        placeholder: true,
        note: t(
          provider.state === "discovering"
            ? "providerDiscovering"
            : provider.error
              ? "providerNoModelsFailed"
              : "providerNoModels",
        ),
        wireId: "",
      });
    }
    for (const m of group) {
      const isProviderModel = !!m.source_id;
      const supportsImages = isProviderModel
        ? m.capabilities?.provenance?.images === "provider" || m.capabilities?.match === "exact"
          ? (m.capabilities?.images ?? null)
          : null
        : (m.supports_images ?? true);
      rows.push({
        key: `builtin:${m.id}`,
        groupKey,
        groupLabel: label,
        groupEndpoint: common,
        name: labels.get(m.id) || m.id,
        modelId: m.id,
        apiShape: (m.api_type && API_TYPE_LABELS[m.api_type]) || null,
        endpoint: common ? "" : m.base_url || "",
        supportsImages,
        imageTitle: isProviderModel
          ? t(
              supportsImages === null
                ? "providerCapabilitiesUnknown"
                : supportsImages
                  ? "imageSupportYes"
                  : "imageSupportNo",
            )
          : supportsImages
            ? t("imageSupportYes")
            : t("imageSupportNo"),
        imageAuto: false,
        model: null,
        provider,
        placeholder: false,
        note: "",
        wireId: m.api_model_name || m.id,
      });
    }
  }
  for (const m of models.value) {
    rows.push({
      key: `custom:${m.model_id}`,
      groupKey: "custom",
      groupLabel: t("customModelsGroup"),
      groupEndpoint: "",
      name: m.display_name,
      modelId: m.model_name,
      apiShape: PROVIDER_LABELS[m.provider_type],
      endpoint: m.endpoint,
      supportsImages: customModelSupportsImages(m),
      imageTitle: customModelImageTitle(m),
      imageAuto: (m.image_support ?? "auto") === "auto",
      model: m,
      provider: null,
      placeholder: false,
      note: "",
      wireId: m.model_name,
    });
  }
  return rows;
});

// Row counts per group for the "(N)" in each group header.
const groupCounts = computed<Record<string, number>>(() => {
  const counts: Record<string, number> = {};
  for (const row of tableRows.value) {
    if (!row.placeholder) counts[row.groupKey] = (counts[row.groupKey] || 0) + 1;
  }
  return counts;
});

// For a custom model, the boolean its image_support setting evaluates to. When
// set to "auto" we use the server-resolved supports_images; explicit yes/no win.
function customModelSupportsImages(model: CustomModel): boolean {
  const setting = model.image_support ?? "auto";
  if (setting === "yes") return true;
  if (setting === "no") return false;
  return model.supports_images ?? true;
}

function customModelImageTitle(model: CustomModel): string {
  const label = customModelSupportsImages(model) ? t("imageSupportYes") : t("imageSupportNo");
  // Surface what auto resolved to for auto models.
  if ((model.image_support ?? "auto") === "auto") {
    return `${t("imageSupportAuto")} \u2014 ${label}`;
  }
  return label;
}

function setBuiltInFromModelList(modelList: AvailableModel[]) {
  builtInModels.value = modelList.filter((m) => (m.source && m.source !== "custom") || m.source_id);
  if (window.__SHELLEY_INIT__) window.__SHELLEY_INIT__.models = modelList;
}

// Reloads can overlap (every action reloads, per-provider refreshes run in
// parallel). Only the newest one may apply its result; the table keeps showing
// the current data while a reload is in flight and the spinner is reserved for
// the first load after the dialog opens.
const loads = latestGate();
let hasLoaded = false;

function errorMessage(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason);
}

async function loadManagementData() {
  const isCurrent = loads.begin();
  if (!hasLoaded) loading.value = true;
  const [customResult, providersResult, modelResult] = await Promise.allSettled([
    customModelsApi.getCustomModels(),
    api.listModelProviders(),
    api.getModels(),
  ]);
  if (!isCurrent()) return;
  const errors: string[] = [];
  if (customResult.status === "fulfilled") models.value = customResult.value;
  else errors.push(errorMessage(customResult.reason));
  if (providersResult.status === "fulfilled") providerConnections.value = providersResult.value;
  else errors.push(errorMessage(providersResult.reason));
  if (modelResult.status === "fulfilled") setBuiltInFromModelList(modelResult.value);
  else errors.push(errorMessage(modelResult.reason));
  loadError.value = errors.length ? errors.join("; ") : null;
  hasLoaded = true;
  loading.value = false;
}

function handleAddNew() {
  editModel.value = null;
  editProvider.value = null;
  formOpen.value = true;
}

function handleEdit(model: CustomModel) {
  editModel.value = model;
  editProvider.value = null;
  formOpen.value = true;
}

function openProviderForm(provider: ModelProviderConnection | null) {
  editModel.value = null;
  editProvider.value = provider;
  formOpen.value = true;
}

// The stacked form dialog saved a model; reload the list and notify the app.
async function handleFormSaved() {
  error.value = null;
  await loadManagementData();
  emit("modelsChanged");
}

async function handleProviderSaved() {
  error.value = null;
  await loadManagementData();
  emit("modelsChanged");
}

async function handleDuplicate(model: CustomModel) {
  let operationError: string | null = null;
  try {
    error.value = null;
    await customModelsApi.duplicateCustomModel(model.model_id);
  } catch (err) {
    operationError = err instanceof Error ? err.message : "Failed to duplicate model";
  } finally {
    await loadManagementData();
    if (operationError) error.value = operationError;
    emit("modelsChanged");
  }
}

async function handleDelete(modelId: string) {
  let operationError: string | null = null;
  try {
    error.value = null;
    await customModelsApi.deleteCustomModel(modelId);
  } catch (err) {
    operationError = err instanceof Error ? err.message : "Failed to delete model";
  } finally {
    await loadManagementData();
    if (operationError) error.value = operationError;
    emit("modelsChanged");
  }
}

async function handleRefreshModels() {
  if (refreshing.value) return;
  refreshing.value = true;
  const isCurrent = loads.begin();
  try {
    error.value = null;
    const response = await api.refreshModelCatalog();
    if (isCurrent()) {
      hasLoaded = true;
      loading.value = false;
      setBuiltInFromModelList(response.models);
      providerConnections.value = response.sources;
      loadError.value = null;
    }
    error.value = response.error ?? null;
  } catch (err) {
    const operationError = err instanceof Error ? err.message : "Failed to refresh models";
    await loadManagementData();
    error.value = operationError;
  } finally {
    refreshing.value = false;
    emit("modelsChanged");
  }
}

async function refreshProvider(provider: ModelProviderConnection) {
  if (refreshingProviderIds.value.has(provider.provider_id)) return;
  refreshingProviderIds.value = new Set(refreshingProviderIds.value).add(provider.provider_id);
  error.value = null;
  let operationError: string | null = null;
  try {
    await api.refreshModelProvider(provider.provider_id);
  } catch (err) {
    operationError = err instanceof Error ? err.message : "Failed to refresh provider connection";
  } finally {
    const refreshingIds = new Set(refreshingProviderIds.value);
    refreshingIds.delete(provider.provider_id);
    refreshingProviderIds.value = refreshingIds;
    await loadManagementData();
    if (operationError) error.value = operationError;
    emit("modelsChanged");
  }
}

async function confirmRemoveProvider() {
  const provider = providerToRemove.value;
  if (!provider || removingProvider.value) return;
  removingProvider.value = true;
  error.value = null;
  removeError.value = null;
  let failure: string | null = null;
  try {
    await api.deleteModelProvider(provider.provider_id);
    closeRemoveProvider();
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      // Already removed (another tab): the list reload below reconciles it.
      closeRemoveProvider();
    } else {
      failure = errorMessage(err);
    }
  } finally {
    removingProvider.value = false;
    await loadManagementData();
    if (failure) removeError.value = failure;
    emit("modelsChanged");
  }
}

watch(
  () => props.refreshTrigger,
  () => {
    if (props.isOpen) void loadManagementData();
  },
);

watch(
  () => props.isOpen,
  (open) => {
    if (open) {
      hasLoaded = false;
      void loadManagementData();
    }
  },
  { immediate: true },
);
</script>
