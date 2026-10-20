import { beforeEach, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent } from "vue";
import { useAuthStore } from "@/stores/auth";
import { useExtensions } from "./useExtensions";
import { createExtensionRegistry, type ExtensionManifest } from "./registry";

vi.mock("@/services/api", () => ({ api: {} }));
vi.mock("@/services/websocket", () => ({ wsService: { disconnect: vi.fn() } }));
beforeEach(() => {
  setActivePinia(createPinia());
  vi.stubGlobal("localStorage", {
    getItem: vi.fn(),
    setItem: vi.fn(),
    removeItem: vi.fn(),
  });
});

it("reactively removes extension navigation and panel hooks on grant removal and logout", () => {
  const Page = defineComponent({ render: () => null });
  const manifest: ExtensionManifest = {
    id: "cases",
    apiVersion: 1,
    version: "1.0.0",
    requires: [],
    routes: [
      {
        id: "assigned",
        path: "assigned",
        title: "Assigned",
        component: "cases/Assigned",
        permission: "cases:work.assigned",
      },
    ],
    navigation: [
      {
        id: "assigned",
        label: "Assigned",
        route: "assigned",
        permission: "cases:work.assigned",
      },
    ],
    panels: [
      {
        id: "context",
        slot: "contact.sidebar",
        component: "cases/Assigned",
        permission: "cases:work.assigned",
      },
    ],
  };
  const registry = createExtensionRegistry(
    [manifest],
    { "cases/Assigned": Page },
    ["cases"],
  );
  const auth = useAuthStore();
  auth.setAuth({
    user: {
      id: "user",
      email: "user@example.test",
      full_name: "User",
      organization_id: "org",
      is_super_admin: true,
      role: { id: "admin", name: "admin", is_system: true, permissions: [] },
    },
  });
  const { navigation, panelsFor } = useExtensions(registry);
  const panels = panelsFor("contact.sidebar");
  expect(navigation.value).toHaveLength(0);
  expect(panels.value).toHaveLength(0);
  auth.user!.role!.permissions = [
    { id: "explicit", resource: "cases", action: "work.assigned" },
  ];
  expect(navigation.value).toHaveLength(1);
  expect(panels.value).toHaveLength(1);
  auth.user!.role!.permissions = [];
  expect(navigation.value).toHaveLength(0);
  expect(panels.value).toHaveLength(0);
  auth.user!.role!.permissions = [
    { id: "explicit", resource: "cases", action: "work.assigned" },
  ];
  auth.clearAuth();
  expect(navigation.value).toHaveLength(0);
  expect(panels.value).toHaveLength(0);
});
