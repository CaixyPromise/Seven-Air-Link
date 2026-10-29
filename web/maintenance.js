"use strict";
// Read-only Owner diagnostics. Every callback is scoped to the current identity.
window.storageReset = () => {
  $("#storage-summary").textContent = "尚未读取";
  $("#storage-size").textContent = "";
  $("#storage-refresh").disabled = false;
};
$("#storage-refresh").addEventListener("click", async () => {
  if (state.me?.role !== "owner") return;
  const epoch = state.authEpoch;
  const button = $("#storage-refresh");
  button.disabled = true;
  try {
    const report = await api("/api/v1/storage");
    if (epoch !== state.authEpoch || state.me?.role !== "owner") return;
    $("#storage-summary").textContent = `SQLite ${report.sqliteVersion} · schema ${report.schema} · ${report.messages} 条消息 · ${report.files} 个有效附件`;
    const mib = n => (Number(n || 0) / 1048576).toFixed(2);
    $("#storage-size").textContent = `数据库 ${mib(report.databaseBytes)} MiB / WAL ${mib(report.walBytes)} MiB / 附件 ${mib(report.fileBytes)} MiB`;
  } catch (e) {
    if (epoch === state.authEpoch) $("#storage-summary").textContent = `读取失败：${e.message}`;
  } finally {
    if (epoch === state.authEpoch) button.disabled = false;
  }
});
