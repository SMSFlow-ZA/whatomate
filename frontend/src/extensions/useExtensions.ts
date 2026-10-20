import { computed } from "vue";
import { useAuthStore } from "@/stores/auth";
import { extensionRegistry } from "./index";
import type { ExtensionPanelSlot, ExtensionRegistry } from "./registry";

/** Reactive discovery for navigation and host-owned panel slots. */
export function useExtensions(registry: ExtensionRegistry = extensionRegistry) {
  const auth = useAuthStore();
  const discovery = computed(() =>
    registry.discover(
      (permission) =>
        auth.isAuthenticated && auth.hasExtensionPermission(permission),
    ),
  );
  return {
    navigation: computed(() => discovery.value.navigation),
    panelsFor: (slot: ExtensionPanelSlot) =>
      computed(() =>
        discovery.value.panels.filter((panel) => panel.slot === slot),
      ),
  };
}
