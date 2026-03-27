import re
with open("/root/CloudHelper/probe_manager/frontend/src/modules/app/components/LinkManageTab.tsx", "r") as f:
    text = f.read()

target = """      setChains(sorted);
      writeProbeLinkChainCache(sorted);
      setChainStatus(`已从主控获取链路（${sorted.length} 条）`);"""

replacement = """      setChains(sorted);
      writeProbeLinkChainCache(sorted);
      setChainStatus(`已从主控获取链路（${sorted.length} 条）`);
      const invoke = (window as any)?.go?.main?.App?.ForceRefreshNetworkAssistantNodes;
      if (typeof invoke === "function") {
        await invoke(props.controllerBaseUrl, props.sessionToken).catch(() => {});
      }"""

if target in text:
    with open("/root/CloudHelper/probe_manager/frontend/src/modules/app/components/LinkManageTab.tsx", "w") as f:
        f.write(text.replace(target, replacement))
    print("Patched loadChainsFromController successfully")
else:
    print("Target not found")
