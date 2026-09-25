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
      </div>
    </template>

    <div class="models-modal">
      <div class="models-tabs-row">
        <div class="models-tabs" role="tablist" aria-label="Model catalogs">
          <button
            id="models-tab"
            class="models-tab"
            type="button"
            role="tab"
            aria-controls="models-panel"
            :aria-selected="activeTab === 'models'"
            :tabindex="activeTab === 'models' ? 0 : -1"
            @click="activeTab = 'models'"
            @keydown="handleTabKeydown"
          >
            Models
          </button>
          <button
            id="transcription-models-tab"
            class="models-tab"
            type="button"
            role="tab"
            aria-controls="transcription-models-panel"
            :aria-selected="activeTab === 'transcription'"
            :tabindex="activeTab === 'transcription' ? 0 : -1"
            @click="activeTab = 'transcription'"
            @keydown="handleTabKeydown"
          >
            Transcription Models
          </button>
        </div>
        <Button
          size="small"
          :disabled="activeTab === 'models' ? loading : transcriptionLoading"
          @click="handleAddForActiveTab"
          >+ {{ t("addModel") }}</Button
        >
      </div>

      <section
        id="models-panel"
        class="models-tab-panel"
        role="tabpanel"
        aria-labelledby="models-tab"
        :hidden="activeTab !== 'models'"
      >
        <div v-if="error" class="models-error">
          {{ error }}
          <button class="models-error-dismiss" @click="error = null">×</button>
        </div>

        <div v-if="loading" class="models-loading">
          <div class="spinner"></div>
          <span>{{ t("loadingModels") }}</span>
        </div>

        <!-- Empty state -->
        <div v-else-if="builtInModels.length === 0 && models.length === 0" class="models-empty">
          <p>{{ t("noModelsConfigured") }}</p>
          <p class="models-empty-hint">{{ t("noModelsHint") }}</p>
        </div>

        <!-- Model List -->
        <DataTable
          v-else
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
            <span class="models-group-name">{{ data.groupLabel }}</span>
            <span class="models-group-count">({{ groupCounts[data.groupKey] }})</span>
            <span v-if="data.groupEndpoint" class="models-group-endpoint">{{
              data.groupEndpoint
            }}</span>
          </template>
          <Column :header="t('columnName')" field="name">
            <template #body="{ data }">
              <span class="models-cell-name">{{ data.name }}</span>
              <span v-for="tag in data.tags" :key="tag" class="models-cell-tag">{{ tag }}</span>
            </template>
          </Column>
          <Column :header="t('columnModelId')" field="modelId">
            <template #body="{ data }">
              <span class="models-cell-mono">{{ data.modelId }}</span>
              <div v-if="data.endpoint" class="models-cell-endpoint" :title="data.endpoint">
                {{ data.endpoint }}
              </div>
            </template>
          </Column>
          <Column :header="t('columnProvider')" field="apiShape">
            <template #body="{ data }">
              <span :class="{ 'models-cell-muted': !data.apiShape }">{{
                data.apiShape || "—"
              }}</span>
            </template>
          </Column>
          <Column :header="t('columnImages')" field="supportsImages" class="models-col-images">
            <template #body="{ data }">
              <span
                :class="data.supportsImages ? 'models-table-image-yes' : 'models-table-image-no'"
                role="img"
                :title="data.imageTitle"
                :aria-label="data.imageTitle"
                >{{ data.supportsImages ? "✓" : "✕"
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
                  <ModelActionIcon kind="duplicate" />
                </Button>
                <Button
                  class="btn-icon"
                  text
                  severity="secondary"
                  v-tooltip.top="t('editModel')"
                  :aria-label="t('editModel')"
                  @click="handleEdit(data.model)"
                >
                  <ModelActionIcon kind="edit" />
                </Button>
                <Button
                  class="btn-icon btn-danger"
                  text
                  severity="danger"
                  v-tooltip.top="t('delete_')"
                  :aria-label="t('delete_')"
                  @click="handleDelete(data.model.model_id)"
                >
                  <ModelActionIcon kind="delete" />
                </Button>
              </div>
            </template>
          </Column>
        </DataTable>
      </section>

      <section
        id="transcription-models-panel"
        class="models-tab-panel"
        role="tabpanel"
        aria-labelledby="transcription-models-tab"
        :hidden="activeTab !== 'transcription'"
      >
        <p class="transcription-models-description">
          Choose one model for transcript generation and one for timecoded transcription. Context
          prompts are optional model capabilities.
        </p>
        <div v-if="transcriptionError" class="models-error" role="alert">
          {{ transcriptionError }}
        </div>
        <div v-if="transcriptionLoading" class="models-loading">
          <div class="spinner" />
          <span>Loading transcription models...</span>
        </div>
        <DataTable
          v-else
          :value="transcriptionRows"
          data-key="model_id"
          size="small"
          scrollable
          scroll-height="flex"
          :dt="modelsTableDt"
          class="models-datatable transcription-models-datatable"
          row-group-mode="subheader"
          group-rows-by="groupKey"
          :pt="{ rowGroupHeaderCell: { colspan: 7 } }"
        >
          <template #groupheader="{ data }">
            <span class="models-group-name">{{ data.groupLabel }}</span>
            <span class="models-group-count">({{ transcriptionGroupCounts[data.groupKey] }})</span>
          </template>
          <Column header="Name" field="display_name"
            ><template #body="{ data }"
              ><span class="models-cell-name">{{ data.display_name }}</span></template
            ></Column
          >
          <Column header="Model ID" field="model_name"
            ><template #body="{ data }"
              ><span class="models-cell-mono">{{ data.model_name }}</span>
              <div class="models-cell-endpoint" :title="data.endpoint">
                {{ data.endpoint }}
              </div></template
            ></Column
          >
          <Column header="Provider" field="provider"
            ><template #body="{ data }"
              ><span>{{ data.provider }}</span
              ><span class="models-cell-muted">
                · {{ protocolLabel(data.protocol) }}</span
              ></template
            ></Column
          >
          <Column header="Transcript" class="transcription-capability-column"
            ><template #body="{ data }"
              ><input
                type="radio"
                name="transcript-transcription-model"
                :checked="isSelected(data, 'transcript')"
                :disabled="selectingRole !== null"
                :aria-label="`Use ${data.display_name} for transcript generation`"
                @change="selectDefault(data.model_id, 'transcript')" /></template
          ></Column>
          <Column header="Context prompts" class="transcription-capability-column"
            ><template #body="{ data }"
              ><span :class="{ 'models-cell-muted': !data.supports_prompted }">{{
                data.supports_prompted ? "Supported" : "Not supported"
              }}</span></template
          ></Column>
          <Column header="Timecodes" class="transcription-capability-column"
            ><template #body="{ data }"
              ><input
                type="radio"
                name="timecoded-transcription-model"
                :checked="isSelected(data, 'timecoded')"
                :disabled="!canUseForRole(data, 'timecoded') || selectingRole !== null"
                :aria-label="`Use ${data.display_name} for timecoded transcription`"
                @change="selectDefault(data.model_id, 'timecoded')" /></template
          ></Column>
          <Column header="Actions" class="models-col-actions"
            ><template #body="{ data }"
              ><div v-if="!data.managed" class="models-cell-actions">
                <Button
                  class="btn-icon"
                  text
                  severity="secondary"
                  v-tooltip.top="t('duplicate')"
                  :aria-label="t('duplicate')"
                  @click="duplicateTranscription(data)"
                  ><ModelActionIcon kind="duplicate" /></Button
                ><Button
                  class="btn-icon"
                  text
                  severity="secondary"
                  v-tooltip.top="t('editModel')"
                  :aria-label="t('editModel')"
                  @click="editTranscription(data)"
                  ><ModelActionIcon kind="edit" /></Button
                ><Button
                  class="btn-icon btn-danger"
                  text
                  severity="danger"
                  v-tooltip.top="t('delete_')"
                  :aria-label="t('delete_')"
                  @click="deleteTranscription(data.model_id)"
                  ><ModelActionIcon kind="delete" /></Button></div></template
          ></Column>
        </DataTable>
      </section>
    </div>
  </Modal>

  <!-- Stacked add/edit dialog, layered on top of the list. -->
  <ModelFormModal
    :is-open="formOpen"
    :edit-model="editModel"
    @saved="handleFormSaved"
    @close="formOpen = false"
  />
  <TranscriptionModelFormModal
    :is-open="transcriptionFormOpen"
    :edit-model="editTranscriptionModel"
    @saved="handleTranscriptionSaved"
    @close="transcriptionFormOpen = false"
  />
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import DataTable from "primevue/datatable";
import Column from "primevue/column";
import Button from "primevue/button";
import Modal from "./Modal.vue";
import ModelActionIcon from "./ModelActionIcon.vue";
import ModelFormModal from "./ModelFormModal.vue";
import TranscriptionModelFormModal from "./TranscriptionModelFormModal.vue";
import { modelsTableDt } from "./modelsTableDt";
import { prettyModelLabels } from "../../utils/modelNames";
import { API_TYPE_LABELS, PROVIDER_LABELS } from "./customModelConstants";
import { useI18n } from "../composables/i18n";
import {
  api,
  customModelsApi,
  transcriptionModelsApi,
  type AvailableModel,
  type CustomModel,
  type TranscriptionModel,
  type TranscriptionRole,
} from "../../services/api";

const props = defineProps<{ isOpen: boolean }>();
const emit = defineEmits<{ (e: "close"): void; (e: "modelsChanged"): void }>();

const { t } = useI18n();

type ModelTab = "models" | "transcription";
const activeTab = ref<ModelTab>("models");

const models = ref<CustomModel[]>([]);
const loading = ref(true);
const refreshing = ref(false);
const error = ref<string | null>(null);
const builtInModels = ref<AvailableModel[]>([]);

// Stacked add/edit dialog state. `editModel` is the custom model being edited,
// or null when adding a new one.
const formOpen = ref(false);
const editModel = ref<CustomModel | null>(null);

const transcriptionModels = ref<TranscriptionModel[]>([]);
const transcriptionDefaults = ref<
  Partial<Record<TranscriptionRole, { model_id: string; available: boolean }>>
>({});
const transcriptionLoading = ref(true);
const transcriptionError = ref<string | null>(null);
const selectingRole = ref<TranscriptionRole | null>(null);
const transcriptionFormOpen = ref(false);
const editTranscriptionModel = ref<TranscriptionModel | null>(null);

const builtInModelsFiltered = computed(() =>
  builtInModels.value.filter((m) => m.id !== "predictable"),
);

const transcriptionRows = computed(() =>
  transcriptionModels.value.map((model) => ({
    ...model,
    groupKey: model.managed ? "managed" : "custom",
    groupLabel: model.managed ? "Managed models" : "Custom models",
  })),
);

const transcriptionGroupCounts = computed<Record<string, number>>(() => {
  const counts: Record<string, number> = {};
  for (const row of transcriptionRows.value) counts[row.groupKey] = (counts[row.groupKey] || 0) + 1;
  return counts;
});

function protocolLabel(protocol: string) {
  return protocol === "openai" ? "OpenAI-compatible" : "Deepgram";
}

function handleTabKeydown(event: KeyboardEvent) {
  if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
  event.preventDefault();
  const tabs = Array.from(
    (event.currentTarget as HTMLButtonElement).parentElement?.querySelectorAll<HTMLButtonElement>(
      '[role="tab"]',
    ) ?? [],
  );
  const currentIndex = tabs.indexOf(event.currentTarget as HTMLButtonElement);
  const nextIndex =
    event.key === "Home"
      ? 0
      : event.key === "End"
        ? tabs.length - 1
        : (currentIndex + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length;
  tabs[nextIndex]?.click();
  tabs[nextIndex]?.focus();
}

function canUseForRole(model: TranscriptionModel, role: TranscriptionRole) {
  if (role === "transcript") return true;
  return model.supports_timecodes;
}

function isSelected(model: TranscriptionModel, role: TranscriptionRole) {
  return (
    canUseForRole(model, role) &&
    transcriptionDefaults.value[role]?.model_id === model.model_id &&
    transcriptionDefaults.value[role]?.available
  );
}

async function loadTranscriptionModels() {
  try {
    transcriptionLoading.value = true;
    transcriptionError.value = null;
    const catalog = await transcriptionModelsApi.list();
    transcriptionModels.value = catalog.models;
    transcriptionDefaults.value = catalog.defaults;
  } catch (err) {
    transcriptionError.value =
      err instanceof Error ? err.message : "Failed to load transcription models";
  } finally {
    transcriptionLoading.value = false;
  }
}

async function selectDefault(modelId: string, role: TranscriptionRole) {
  try {
    selectingRole.value = role;
    transcriptionError.value = null;
    const selected = await transcriptionModelsApi.setDefault(role, modelId);
    transcriptionDefaults.value = { ...transcriptionDefaults.value, [role]: selected };
  } catch (err) {
    transcriptionError.value =
      err instanceof Error ? err.message : "Failed to select transcription model";
  } finally {
    selectingRole.value = null;
  }
}

function handleAddTranscription() {
  editTranscriptionModel.value = null;
  transcriptionFormOpen.value = true;
}

function handleAddForActiveTab() {
  if (activeTab.value === "transcription") {
    handleAddTranscription();
    return;
  }
  handleAddNew();
}

function editTranscription(model: TranscriptionModel) {
  editTranscriptionModel.value = model;
  transcriptionFormOpen.value = true;
}

async function handleTranscriptionSaved() {
  await loadTranscriptionModels();
}

async function duplicateTranscription(model: TranscriptionModel) {
  try {
    transcriptionError.value = null;
    await transcriptionModelsApi.duplicate(model.model_id);
    await loadTranscriptionModels();
  } catch (err) {
    transcriptionError.value =
      err instanceof Error ? err.message : "Failed to duplicate transcription model";
  }
}

async function deleteTranscription(modelId: string) {
  try {
    transcriptionError.value = null;
    await transcriptionModelsApi.delete(modelId);
    await loadTranscriptionModels();
  } catch (err) {
    transcriptionError.value =
      err instanceof Error ? err.message : "Failed to delete transcription model";
  }
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
  tags: string[];
  supportsImages: boolean;
  imageTitle: string;
  imageAuto: boolean;
  model: CustomModel | null;
}

const tableRows = computed<TableRow[]>(() => {
  const labels = prettyModelLabels(builtInModelsFiltered.value);
  // Group built-ins by source, preserving catalog order within and across
  // groups (first appearance wins).
  const groups = new Map<string, AvailableModel[]>();
  for (const m of builtInModelsFiltered.value) {
    const src = m.source || "";
    if (!groups.has(src)) groups.set(src, []);
    groups.get(src)!.push(m);
  }
  const rows: TableRow[] = [];
  for (const [src, group] of groups) {
    // Endpoint shared by every model in the group renders once in the group
    // header; otherwise it stays per-row.
    const endpoints = new Set(group.map((m) => m.base_url || ""));
    const common = endpoints.size === 1 ? (group[0].base_url ?? "") : "";
    for (const m of group) {
      rows.push({
        key: `builtin:${m.id}`,
        groupKey: `builtin:${src}`,
        groupLabel: src,
        groupEndpoint: common,
        name: labels.get(m.id) || m.id,
        modelId: m.id,
        apiShape: (m.api_type && API_TYPE_LABELS[m.api_type]) || null,
        endpoint: common ? "" : m.base_url || "",
        tags: [],
        supportsImages: m.supports_images ?? true,
        imageTitle: (m.supports_images ?? true) ? t("imageSupportYes") : t("imageSupportNo"),
        imageAuto: false,
        model: null,
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
      tags: (m.tags || "")
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean),
      supportsImages: customModelSupportsImages(m),
      imageTitle: customModelImageTitle(m),
      imageAuto: (m.image_support ?? "auto") === "auto",
      model: m,
    });
  }
  return rows;
});

// Row counts per group for the "(N)" in each group header.
const groupCounts = computed<Record<string, number>>(() => {
  const counts: Record<string, number> = {};
  for (const row of tableRows.value) {
    counts[row.groupKey] = (counts[row.groupKey] || 0) + 1;
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

async function loadModels() {
  try {
    loading.value = true;
    error.value = null;
    models.value = await customModelsApi.getCustomModels();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to load models";
  } finally {
    loading.value = false;
  }
}

function setBuiltInFromModelList(modelList: AvailableModel[]) {
  builtInModels.value = modelList.filter((m) => m.source && m.source !== "custom");
}

function handleAddNew() {
  editModel.value = null;
  formOpen.value = true;
}

function handleEdit(model: CustomModel) {
  editModel.value = model;
  formOpen.value = true;
}

// The stacked form dialog saved a model; reload the list and notify the app.
async function handleFormSaved() {
  await loadModels();
  emit("modelsChanged");
}

async function handleDuplicate(model: CustomModel) {
  try {
    error.value = null;
    await customModelsApi.duplicateCustomModel(model.model_id);
    await loadModels();
    emit("modelsChanged");
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to duplicate model";
  }
}

async function handleDelete(modelId: string) {
  try {
    error.value = null;
    await customModelsApi.deleteCustomModel(modelId);
    await loadModels();
    emit("modelsChanged");
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to delete model";
  }
}

async function handleRefreshModels() {
  try {
    refreshing.value = true;
    error.value = null;
    const refreshedModels = await api.refreshModels();
    if (window.__SHELLEY_INIT__) {
      window.__SHELLEY_INIT__.models = refreshedModels;
    }
    setBuiltInFromModelList(refreshedModels);
    emit("modelsChanged");
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to refresh models";
  } finally {
    refreshing.value = false;
  }
}

watch(
  () => props.isOpen,
  (open) => {
    if (open) {
      activeTab.value = "models";
      loadModels();
      loadTranscriptionModels();
      const initData = window.__SHELLEY_INIT__;
      if (initData?.models) {
        setBuiltInFromModelList(initData.models);
      }
    }
  },
  { immediate: true },
);
</script>
