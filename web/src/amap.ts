import AMapLoader from "@amap/amap-jsapi-loader";

declare global {
  interface Window {
    _AMapSecurityConfig?: {
      securityJsCode: string;
    };
  }
}

let pendingLoad: Promise<typeof AMap> | null = null;

export function hasAMapKey(): boolean {
  return readEnvironment("VITE_AMAP_KEY").length > 0;
}

export function loadAMap(): Promise<typeof AMap> {
  if (pendingLoad !== null) {
    return pendingLoad;
  }
  const key = readEnvironment("VITE_AMAP_KEY");
  if (key.length === 0) {
    return Promise.reject(new Error("未配置高德地图 key"));
  }
  const securityCode = readEnvironment("VITE_AMAP_SECURITY_JS_CODE");
  if (securityCode.length > 0) {
    window._AMapSecurityConfig = { securityJsCode: securityCode };
  }
  pendingLoad = AMapLoader.load({
    key,
    version: "2.0",
    plugins: [],
  }).catch((error: unknown) => {
    pendingLoad = null;
    throw error;
  });
  return pendingLoad;
}

function readEnvironment(name: "VITE_AMAP_KEY" | "VITE_AMAP_SECURITY_JS_CODE"): string {
  const value = import.meta.env[name];
  return typeof value === "string" ? value.trim() : "";
}
