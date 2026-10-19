// Shared "which model do we start with" logic. Both the composer and the
// customized-build rebase action need a *ready* model id, seeded from the
// user's last pick, and both must agree on where that pick is stored.
import type { Model } from "../../types";
import { canSendWithModel } from "../../utils/modelSetupHint";

export const SELECTED_MODEL_KEY = "shelley_selected_model";

export function storedSelectedModel(): string | undefined {
  return localStorage.getItem(SELECTED_MODEL_KEY) || undefined;
}

/** The model the server uses when none is named, as a profile's "" model. */
export function serverDefaultModel(): string {
  return window.__SHELLEY_INIT__?.default_model || "";
}

// pickReadyModel resolves the model to start with: the caller's preference if
// it is ready or resolves through a saved connection, else a ready server default or any ready
// model. "" when the catalog serves nothing ready — deliberately no invented
// fallback id, which would turn "no models configured" into a confusing
// "Unsupported model" from the server.
export function pickReadyModel(
  models: Pick<Model, "id" | "ready">[],
  preferred?: string,
  providerIds?: readonly string[],
): string {
  const ready = (id?: string) => !!id && models.some((m) => m.id === id && m.ready);
  if (
    preferred &&
    canSendWithModel(
      preferred,
      models.filter((m) => m.ready).map((m) => m.id),
      providerIds,
    )
  ) {
    return preferred;
  }
  if (preferred?.startsWith("provider:")) return "";
  if (ready(serverDefaultModel())) return serverDefaultModel();
  return models.find((m) => m.ready)?.id || "";
}
