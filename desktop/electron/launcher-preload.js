'use strict'
// 启动器设置窗口的 preload：只暴露一组白名单方法，渲染进程拿不到 Node。
const { contextBridge, ipcRenderer } = require('electron')

contextBridge.exposeInMainWorld('launcher', {
  getState: () => ipcRenderer.invoke('launcher:get-state'),
  save: (cfg) => ipcRenderer.invoke('launcher:save', cfg),
  pickBinary: () => ipcRenderer.invoke('launcher:pick-binary'),
  pickDir: () => ipcRenderer.invoke('launcher:pick-dir'),
  startService: () => ipcRenderer.invoke('launcher:start-service'),
  stopService: () => ipcRenderer.invoke('launcher:stop-service'),
  restartService: () => ipcRenderer.invoke('launcher:restart-service'),
  openConsole: () => ipcRenderer.invoke('launcher:open-console'),
  openDataDir: () => ipcRenderer.invoke('launcher:open-data-dir'),
  resetDefaults: () => ipcRenderer.invoke('launcher:reset'),
  close: () => ipcRenderer.invoke('launcher:close'),
})
