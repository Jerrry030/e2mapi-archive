(function () {
  "use strict";

  var API = {
    config: "/api/local/connector/config",
    gatewayTest: "/api/local/connector/test",
    coreTest: "/api/local/connector/core-test",
    diagnostics: "/api/local/connector/diagnostics"
  };
  var AUTH_BY_KIND = { sub2api: "x-api-key", newapi: "newapi", cpa: "bearer" };
  clearLegacyFragment();
  var configured = {};
  var containerMode = false;
  var state = {
    authorized: false,
    configLoading: false,
    configReady: false,
    diagnosticsLoading: false,
    savedConfig: null,
    savedFingerprint: "",
    candidateTest: null,
    busy: ""
  };

  var form = document.getElementById("configForm");
  var loadingState = document.getElementById("loadingState");
  var authGate = document.getElementById("authGate");
  var authGateTitle = document.getElementById("authGateTitle");
  var authGateKicker = authGate.querySelector(".section-kicker");
  var authGateMessage = authGate.querySelector(".gate-copy > p:not(.section-kicker)");
  var configContent = document.getElementById("configContent");
  var configFieldset = document.getElementById("configFieldset");
  var dirtyBanner = document.getElementById("dirtyBanner");
  var dirtyMessage = document.getElementById("dirtyMessage");
  var discardButton = document.getElementById("discardButton");
  var gatewayKind = document.getElementById("gatewayKind");
  var gatewayUrl = document.getElementById("gatewayUrl");
  var authLabel = document.getElementById("authLabel");
  var requestTimeout = document.getElementById("requestTimeout");
  var logLevelInputs = Array.prototype.slice.call(document.querySelectorAll("input[name='logLevel']"));
  var xApiKey = document.getElementById("xApiKey");
  var newapiUser = document.getElementById("newapiUser");
  var newapiToken = document.getElementById("newapiToken");
  var bearerToken = document.getElementById("bearerToken");
  var clearXApiKey = document.getElementById("clearXApiKey");
  var clearNewapiUser = document.getElementById("clearNewapiUser");
  var clearNewapiToken = document.getElementById("clearNewapiToken");
  var clearBearerToken = document.getElementById("clearBearerToken");
  var testButton = document.getElementById("testButton");
  var coreTestButton = document.getElementById("coreTestButton");
  var saveButton = document.getElementById("saveButton");
  var bannerSaveButton = document.getElementById("bannerSaveButton");
  var statusBox = document.getElementById("statusBox");
  var statusTitle = document.getElementById("statusTitle");
  var statusMessage = document.getElementById("statusMessage");
  var modeBadge = document.getElementById("modeBadge");
  var candidateStatus = document.getElementById("candidateStatus");
  var candidateTestResult = document.getElementById("candidateTestResult");
  var candidateTestMeta = document.getElementById("candidateTestMeta");
  var previewKind = document.getElementById("previewKind");
  var previewUrl = document.getElementById("previewUrl");
  var previewAuth = document.getElementById("previewAuth");
  var previewCredential = document.getElementById("previewCredential");
  var savedStatus = document.getElementById("savedStatus");
  var savedKind = document.getElementById("savedKind");
  var savedUrl = document.getElementById("savedUrl");
  var savedAuth = document.getElementById("savedAuth");
  var savedCredential = document.getElementById("savedCredential");
  var savedUpdatedAt = document.getElementById("savedUpdatedAt");
  var runtimeHealth = document.getElementById("runtimeHealth");
  var runtimeHealthTitle = document.getElementById("runtimeHealthTitle");
  var runtimeHealthMessage = document.getElementById("runtimeHealthMessage");
  var diagLastSuccess = document.getElementById("diagLastSuccess");
  var diagEmpty = document.getElementById("diagEmpty");
  var gatewayUrlError = document.getElementById("gatewayUrlError");
  var requestTimeoutError = document.getElementById("requestTimeoutError");
  var credentialError = document.getElementById("credentialError");
  var formError = document.getElementById("formError");
  var actionHint = document.getElementById("actionHint");
  var clearConfirmDialog = document.getElementById("clearConfirmDialog");
  var clearConfirmList = document.getElementById("clearConfirmList");
  var cancelClearButton = document.getElementById("cancelClearButton");
  var confirmClearButton = document.getElementById("confirmClearButton");
  var authPanels = Array.prototype.slice.call(document.querySelectorAll("[data-auth-panel]"));
  var secretToggles = Array.prototype.slice.call(document.querySelectorAll("[data-secret-toggle]"));
  var authRetryButton = document.createElement("button");
  authRetryButton.type = "button";
  authRetryButton.className = "btn secondary";
  authRetryButton.textContent = "重试读取配置";
  authRetryButton.hidden = true;
  authGate.querySelector(".gate-copy").appendChild(authRetryButton);
  renderAccessGate("load-error", "");

  function clearLegacyFragment() {
    if (window.location.hash) {
      window.history.replaceState(null, document.title, window.location.pathname + window.location.search);
    }
  }

  function selectedAuth() { return AUTH_BY_KIND[gatewayKind.value] || "x-api-key"; }

  function renderAccessGate(mode, message) {
    var loadError = mode === "load-error";
    authGate.dataset.mode = mode;
    authGateKicker.textContent = loadError ? "本地配置读取失败" : "本地会话已失效";
    authGateTitle.textContent = loadError ? "未能读取 Connector 的已保存配置" : "请刷新页面重新进入本地配置";
    authGateMessage.textContent = loadError ? (message + " 你可以重试；页面加载失败不会修改已保存配置。") : "Connector 会在刷新页面时自动重新建立本地会话，无需输入访问令牌。";
    authRetryButton.textContent = loadError ? "重试读取配置" : "刷新页面";
    authRetryButton.hidden = false;
  }

  function selectedLogLevel() {
    var selected = logLevelInputs.find(function (input) { return input.checked; });
    return selected ? selected.value : "info";
  }

  function setLogLevel(value) {
    logLevelInputs.forEach(function (input) { input.checked = input.value === value; });
  }

  function updateGatewayKind() {
    var auth = selectedAuth();
    authLabel.textContent = auth;
    authPanels.forEach(function (panel) {
      panel.hidden = panel.getAttribute("data-auth-panel") !== auth;
    });
    updatePreview();
    if (!state.configLoading) { refreshUI(); }
  }

  function relevantCredentialFields() {
    switch (selectedAuth()) {
      case "newapi": return [
        { name: "newapi_user_id", input: newapiUser, clear: clearNewapiUser },
        { name: "newapi_token", input: newapiToken, clear: clearNewapiToken }
      ];
      case "bearer": return [{ name: "bearer_token", input: bearerToken, clear: clearBearerToken }];
      default: return [{ name: "x_api_key", input: xApiKey, clear: clearXApiKey }];
    }
  }

  function normalizedOrigin(value) {
    try {
      var parsed = new URL(value);
      if (parsed.protocol !== "http:" && parsed.protocol !== "https:") { return ""; }
      return parsed.protocol + "//" + parsed.host.toLowerCase();
    } catch (error) { return ""; }
  }

  function credentialScopeMatchesSaved() {
    var saved = state.savedConfig;
    return Boolean(saved && saved.gateway_kind === gatewayKind.value && saved.auth === selectedAuth() &&
      normalizedOrigin(saved.gateway_url) && normalizedOrigin(saved.gateway_url) === normalizedOrigin(gatewayUrl.value.trim()));
  }

  function hasRetainedCredential(name) {
    return credentialScopeMatchesSaved() && configured[name];
  }

  function updatePreview() {
    var option = gatewayKind.options[gatewayKind.selectedIndex];
    previewKind.textContent = option ? option.textContent : gatewayKind.value;
    previewUrl.textContent = gatewayUrl.value.trim() || "未填写";
    previewAuth.textContent = selectedAuth();
    var relevant = relevantCredentialFields();
    var active = relevant.filter(function (field) {
      return field.input.value.trim() || (hasRetainedCredential(field.name) && !field.clear.checked);
    }).length;
    previewCredential.textContent = active === relevant.length ? (relevant.some(function (field) { return field.input.value.trim(); }) ? "待更新" : "沿用已保存值") : "需要重新输入";
  }

  function collectPayload() {
    var payload = {
      gateway_kind: gatewayKind.value,
      gateway_url: gatewayUrl.value.trim().replace(/\/+$/, ""),
      auth: selectedAuth(),
      credentials: {},
      clear_credentials: {},
      runtime: {
        gateway_request_timeout_seconds: Number(requestTimeout.value),
        log_level: selectedLogLevel()
      }
    };
    relevantCredentialFields().forEach(function (field) {
      if (field.input.value.trim()) { payload.credentials[field.name] = field.input.value.trim(); }
      if (field.clear.checked) { payload.clear_credentials[field.name] = true; }
    });
    return payload;
  }

  function fingerprintHash(value) {
    var hash = 2166136261;
    for (var index = 0; index < value.length; index += 1) {
      hash ^= value.charCodeAt(index);
      hash = Math.imul(hash, 16777619);
    }
    return (hash >>> 0).toString(36);
  }

  function payloadFingerprint(payload) {
    Object.keys(payload.credentials).forEach(function (name) {
      payload.credentials[name] = "present:" + payload.credentials[name].length + ":" + fingerprintHash(payload.credentials[name]);
    });
    return JSON.stringify(payload);
  }

  function candidateFingerprint() { return payloadFingerprint(collectPayload()); }

  function candidateFingerprintWithoutClears() {
    var payload = collectPayload();
    payload.clear_credentials = {};
    return payloadFingerprint(payload);
  }

  function formatTime(value) {
    if (!value) { return "暂无记录"; }
    var date = new Date(value);
    return isNaN(date.getTime()) ? "暂无记录" : date.toLocaleString();
  }

  function credentialSummary(cfg) {
    var flags = cfg && cfg.credential_configured || {};
    var auth = cfg && cfg.auth || selectedAuth();
    var names = auth === "newapi" ? ["newapi_user_id", "newapi_token"] : auth === "bearer" ? ["bearer_token"] : ["x_api_key"];
    return names.every(function (name) { return flags[name]; }) ? "已保存（不会回显）" : "未完整配置";
  }

  function renderSavedConfig(cfg) {
    var option = Array.prototype.slice.call(gatewayKind.options).find(function (item) { return item.value === cfg.gateway_kind; });
    savedKind.textContent = option ? option.textContent : (cfg.gateway_kind || "未配置");
    savedUrl.textContent = cfg.gateway_url || "未填写";
    savedAuth.textContent = cfg.auth || "未配置";
    savedCredential.textContent = credentialSummary(cfg);
    savedUpdatedAt.textContent = cfg.updated_at ? formatTime(cfg.updated_at) : "尚未保存";
    savedStatus.textContent = cfg.gateway_configured ? "已配置" : "未完整配置";
    savedStatus.setAttribute("data-tone", cfg.gateway_configured ? "ok" : "warn");
  }

  function currentDirty() {
    return state.configReady && candidateFingerprint() !== state.savedFingerprint;
  }

  function setPill(element, tone, text) {
    element.setAttribute("data-tone", tone || "idle");
    element.textContent = text;
  }

  function refreshUI() {
    var ready = state.authorized && state.configReady && !state.configLoading;
    loadingState.hidden = !state.configLoading;
    authGate.hidden = ready || state.configLoading;
    configContent.hidden = !ready;
    configFieldset.disabled = !ready || Boolean(state.busy);
    var dirty = ready && currentDirty();
    var currentFingerprint = candidateFingerprint();
    var currentTest = state.candidateTest && state.candidateTest.fingerprint === currentFingerprint;
    var clearRequested = clearLabels(collectPayload()).length > 0;
    var clearOnlyChange = clearRequested && candidateFingerprintWithoutClears() === state.savedFingerprint;
    var canSave = dirty && (clearOnlyChange || currentTest && state.candidateTest.ok);
    dirtyBanner.hidden = !dirty;
    if (dirty) {
      dirtyMessage.textContent = clearOnlyChange ? "删除凭据无需连接测试，保存时需确认。" :
        !state.candidateTest ? "当前更改尚未保存。" :
        !currentTest ? "测试结果已过期，请重新测试。" :
          state.candidateTest.ok ? "测试已通过，尚未保存。" : "测试失败，请修正后重试。";
    }
    var hasURL = Boolean(gatewayUrl.value.trim());
    testButton.disabled = !ready || Boolean(state.busy) || !hasURL;
    var saveDisabled = !ready || Boolean(state.busy) || !canSave;
    saveButton.disabled = saveDisabled;
    bannerSaveButton.disabled = saveDisabled;
    coreTestButton.disabled = !ready || Boolean(state.busy);
    actionHint.textContent = !ready ? "正在读取配置。" : !hasURL ? "请先填写管理地址。" : !dirty ? "当前配置没有更改。" : clearRequested && !clearOnlyChange ? "请先取消删除，测试并保存其他更改。" : clearOnlyChange ? "删除凭据需确认后保存。" : currentTest && state.candidateTest.ok ? "测试通过，可保存。" : "请先测试当前配置。";
    testButton.title = testButton.disabled ? actionHint.textContent : "测试当前候选配置，不写入磁盘";
    var saveTitle = saveDisabled ? actionHint.textContent : "保存并立即应用到 Connector";
    saveButton.title = saveTitle;
    bannerSaveButton.title = saveTitle;
    if (clearOnlyChange) {
      setPill(candidateStatus, "warn", "待确认删除凭据");
      candidateTestResult.textContent = "无需连接测试";
      candidateTestMeta.textContent = "保存时需再次确认";
    } else if (state.candidateTest && !currentTest) {
      setPill(candidateStatus, "warn", "测试已过期");
      candidateTestResult.textContent = "候选值已更改，请重新测试";
      candidateTestMeta.textContent = "";
    } else if (!state.candidateTest) {
      setPill(candidateStatus, dirty ? "warn" : "idle", dirty ? "有未保存更改" : "与已保存一致");
    }
  }

  function clearValidation() {
    [gatewayUrlError, requestTimeoutError, credentialError].forEach(function (element) { element.hidden = true; element.textContent = ""; });
    [gatewayUrl, requestTimeout].forEach(function (input) { input.removeAttribute("aria-invalid"); });
    formError.hidden = true;
    formError.querySelector("p").textContent = "";
  }

  function showValidation(errors) {
    clearValidation();
    errors.forEach(function (error) {
      var target = error.field === "url" ? gatewayUrlError : error.field === "timeout" ? requestTimeoutError : credentialError;
      target.textContent = error.message;
      target.hidden = false;
      if (error.field === "url") { gatewayUrl.setAttribute("aria-invalid", "true"); }
      if (error.field === "timeout") { requestTimeout.setAttribute("aria-invalid", "true"); }
    });
    if (errors.length) {
      formError.querySelector("p").textContent = errors.map(function (error) { return error.message; }).join("；");
      formError.hidden = false;
    }
  }

  function validatePayload(payload, allowClear) {
    var errors = [];
    var parsed;
    if (!payload.gateway_url) {
      errors.push({ field: "url", message: "请填写管理地址。" });
    } else {
      try {
        parsed = new URL(payload.gateway_url);
        if (parsed.protocol !== "http:" && parsed.protocol !== "https:") { errors.push({ field: "url", message: "管理地址必须使用 http 或 https。" }); }
        if (parsed.username || parsed.password || parsed.search || parsed.hash) { errors.push({ field: "url", message: "管理地址不能包含用户信息、查询参数或 fragment。" }); }
        if (containerMode && isLoopbackHost(parsed.hostname)) {
          errors.push({ field: "url", message: "Docker 中的 127.0.0.1/localhost 指向 Connector 容器自身；请填写容器可访问的服务地址。" });
        }
      } catch (error) { errors.push({ field: "url", message: "管理地址格式不正确。" }); }
    }
    if (!Number.isInteger(payload.runtime.gateway_request_timeout_seconds) ||
        payload.runtime.gateway_request_timeout_seconds < 5 || payload.runtime.gateway_request_timeout_seconds > 20) {
      errors.push({ field: "timeout", message: "网关请求超时必须是 5 到 20 秒的整数。" });
    }
    relevantCredentialFields().forEach(function (field) {
      if (!field.input.value.trim() && (!hasRetainedCredential(field.name) || field.clear.checked) && !(allowClear && field.clear.checked)) {
        errors.push({ field: "credential", message: "请填写所需凭据，或保留当前已保存凭据。" });
      }
    });
    return errors;
  }

  var ERROR_MESSAGES = {
    gateway_auth_failed: "网关拒绝了凭据，请检查 API Key 或 Token。",
    gateway_redirect_rejected: "网关返回了重定向。请填写最终管理地址，并检查 HTTP/HTTPS 配置。",
    gateway_resource_not_found: "未找到管理 API，请检查网关根地址和版本。",
    gateway_rejected: "网关暂时拒绝请求，请稍后重试。",
    gateway_unavailable: "网关服务暂不可用，请检查服务状态。",
    gateway_timeout: "连接网关超时，请检查网络或调整超时设置。",
    gateway_unreachable: "无法连接网关，请检查地址、DNS 与容器网络。",
    gateway_test_failed: "网关测试失败，请检查候选配置。"
  };

  function friendlyError(error) {
    return error && error.errorCode && ERROR_MESSAGES[error.errorCode] || error.message || "操作失败，请重试。";
  }

  function isLoopbackHost(hostname) {
    var host = (hostname || "").toLowerCase().replace(/^\[|\]$/g, "");
    return host === "localhost" || host === "::1" || /^127(?:\.\d{1,3}){3}$/.test(host);
  }

  function setStatus(tone, title, message) {
    statusBox.setAttribute("data-tone", tone || "idle");
    statusTitle.textContent = title;
    statusMessage.textContent = message;
  }

  function setBusy(button, busy, text) {
    if (!button.dataset.defaultText) { button.dataset.defaultText = button.textContent; }
    state.busy = busy ? button.id : "";
    button.dataset.busy = busy ? "true" : "false";
    button.textContent = busy ? text : button.dataset.defaultText;
    refreshUI();
  }

  function setAuthorized(authorized) {
    state.authorized = authorized;
    modeBadge.textContent = authorized ? "已就绪" : "暂不可用";
    modeBadge.setAttribute("data-tone", authorized ? "ok" : "warn");
    refreshUI();
  }

  function clearAuthorization() {
    setAuthorized(false);
    state.configLoading = false;
    state.configReady = false;
    renderAccessGate("session-error", "");
    refreshUI();
  }

  function toggleSecretVisibility(button) {
    var input = document.getElementById(button.getAttribute("data-secret-toggle"));
    if (!input) { return; }
    var reveal = input.type === "password";
    input.type = reveal ? "text" : "password";
    button.querySelector(".icon-eye").hidden = reveal;
    button.querySelector(".icon-eye-off").hidden = !reveal;
    button.setAttribute("aria-label", (reveal ? "隐藏" : "显示") + "凭据");
    button.setAttribute("title", (reveal ? "隐藏" : "显示") + "凭据");
  }

  function resetSecretVisibility(input) {
    var button = document.querySelector('[data-secret-toggle="' + input.id + '"]');
    if (!button) { return; }
    input.type = "password";
    button.querySelector(".icon-eye").hidden = false;
    button.querySelector(".icon-eye-off").hidden = true;
    button.setAttribute("aria-label", "显示凭据");
    button.setAttribute("title", "显示凭据");
  }

  function resetCredentialControls() {
    [[xApiKey, clearXApiKey], [newapiUser, clearNewapiUser], [newapiToken, clearNewapiToken], [bearerToken, clearBearerToken]].forEach(function (pair) {
      pair[1].checked = false;
      pair[0].disabled = false;
      var toggle = document.querySelector('[data-secret-toggle="' + pair[0].id + '"]');
      if (toggle) { toggle.disabled = false; }
    });
  }

  function requestJSON(path, options) {
    options = options || {};
    options.headers = options.headers || {};
    options.credentials = "same-origin";
    return window.fetch(path, options).then(function (response) {
      return response.text().then(function (text) {
        var data = {};
        try { data = text ? JSON.parse(text) : {}; } catch (error) { data = {}; }
        if (!response.ok) {
          if (response.status === 401) {
            clearAuthorization();
            var authorizationError = new Error("本地会话缺失或已失效，请刷新页面重新进入。");
            authorizationError.httpStatus = 401;
            throw authorizationError;
          }
          var requestError = new Error(data.error || data.message || ("HTTP " + response.status));
          requestError.errorCode = data.error_code || "";
          requestError.httpStatus = response.status;
          throw requestError;
        }
        return data;
      });
    });
  }

  function postJSON(path, payload) {
    return requestJSON(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload || {}) });
  }

  function formatDiagnostic(result) {
    if (!result || result.status === "unknown") { return "暂无记录"; }
    var parts = [result.status === "ok" ? "正常" : "失败"];
    if (result.checked_at) { parts.push(formatTime(result.checked_at)); }
    if (result.latency_ms) { parts.push(result.latency_ms + " ms"); }
    if (result.http_status) { parts.push("HTTP " + result.http_status); }
    if (result.failure_count) { parts.push("连续 " + result.failure_count + " 次"); }
    if (result.next_retry_at) { parts.push("下次 " + new Date(result.next_retry_at).toLocaleTimeString()); }
    if (result.error_code) { parts.push(ERROR_MESSAGES[result.error_code] || result.error_code); }
    return parts.join(" · ");
  }

  function renderDiagnostics(data) {
    var entries = [data.core_sync, data.core_test, data.gateway_request, data.gateway_test];
    document.getElementById("diagCoreSync").textContent = formatDiagnostic(data.core_sync);
    document.getElementById("diagCoreTest").textContent = formatDiagnostic(data.core_test);
    document.getElementById("diagGatewayRequest").textContent = formatDiagnostic(data.gateway_request);
    document.getElementById("diagGatewayTest").textContent = formatDiagnostic(data.gateway_test);
    diagEmpty.hidden = entries.some(function (entry) { return entry && entry.status !== "unknown"; });
    var runtimeEntries = [data.core_sync, data.core_test, data.gateway_request];
    var successes = runtimeEntries.map(function (entry) { return entry && entry.last_success_at ? new Date(entry.last_success_at) : null; })
      .filter(function (date) { return date && !isNaN(date.getTime()); })
      .sort(function (left, right) { return right - left; });
    diagLastSuccess.textContent = successes.length ? successes[0].toLocaleString() : "暂无记录";
    var runtime = data.gateway_request;
    if (!runtime || runtime.status === "unknown") {
      runtimeHealth.setAttribute("data-tone", "idle");
      runtimeHealthTitle.textContent = "暂无运行记录";
      runtimeHealthMessage.textContent = "";
    } else if (runtime.status === "ok") {
      runtimeHealth.setAttribute("data-tone", "ok");
      runtimeHealthTitle.textContent = "已保存配置运行正常";
      runtimeHealthMessage.textContent = "最近成功：" + formatTime(runtime.last_success_at || runtime.checked_at);
    } else {
      runtimeHealth.setAttribute("data-tone", "error");
      runtimeHealthTitle.textContent = "已保存配置最近请求失败";
      runtimeHealthMessage.textContent = ERROR_MESSAGES[runtime.error_code] || "请查看下方脱敏诊断并检查网关服务。";
    }
  }

  function loadDiagnostics() {
    state.diagnosticsLoading = true;
    return requestJSON(API.diagnostics).then(renderDiagnostics).catch(function () {
      runtimeHealth.setAttribute("data-tone", "warn");
      runtimeHealthTitle.textContent = "诊断暂不可用";
      runtimeHealthMessage.textContent = "配置仍可编辑和保存；稍后可重新测试以刷新诊断。";
    }).finally(function () { state.diagnosticsLoading = false; });
  }

  function applyConfig(cfg) {
    containerMode = cfg.container_mode === true;
    configured = cfg.credential_configured || {};
    state.savedConfig = cfg;
    gatewayKind.value = AUTH_BY_KIND[cfg.gateway_kind] ? cfg.gateway_kind : "sub2api";
    gatewayUrl.value = cfg.gateway_url || "";
    requestTimeout.value = cfg.runtime && cfg.runtime.gateway_request_timeout_seconds || 15;
    setLogLevel(cfg.runtime && cfg.runtime.log_level || "info");
    updateGatewayKind();
    state.savedFingerprint = candidateFingerprint();
    renderSavedConfig(cfg);
    state.candidateTest = null;
    setPill(candidateStatus, "idle", "尚未测试");
    candidateTestResult.textContent = "尚未测试";
    candidateTestMeta.textContent = "";
    refreshUI();
  }

  function loadConfig() {
    state.configLoading = true;
    state.configReady = false;
    setAuthorized(false);
    modeBadge.textContent = "正在读取";
    modeBadge.setAttribute("data-tone", "loading");
    refreshUI();
    requestJSON(API.config).then(function (cfg) {
      state.authorized = true;
      state.configReady = true;
      applyConfig(cfg);
      setStatus(cfg.gateway_configured ? "ok" : "warn", cfg.gateway_configured ? "已加载配置" : "配置尚未完整", "");
      loadDiagnostics();
    }).catch(function (error) {
      if (error.httpStatus !== 401) {
        state.authorized = false;
        renderAccessGate("load-error", friendlyError(error));
        setStatus("error", "读取配置失败", friendlyError(error));
      }
    }).finally(function () {
      state.configLoading = false;
      modeBadge.textContent = state.configReady ? "已就绪" : "暂不可用";
      modeBadge.setAttribute("data-tone", state.configReady ? "ok" : "warn");
      refreshUI();
    });
  }

  function handleGatewayTest() {
    var payload = collectPayload();
    var errors = validatePayload(payload, false);
    showValidation(errors);
    if (errors.length) { setStatus("error", "候选配置不完整", "请修正表单中标出的字段后重试。"); formError.focus(); return; }
    var fingerprint = candidateFingerprint();
    setBusy(testButton, true, "测试中");
    postJSON(API.gatewayTest, payload).then(function (data) {
      state.candidateTest = { ok: true, fingerprint: fingerprint, at: new Date() };
      setPill(candidateStatus, "ok", "测试通过 · 未保存");
      candidateTestResult.textContent = "通过 · 未保存";
      candidateTestMeta.textContent = "测试时间 " + state.candidateTest.at.toLocaleString();
      setStatus("ok", "候选网关可用", data.message || "候选配置测试通过，尚未保存。");
    }).catch(function (error) {
      state.candidateTest = { ok: false, fingerprint: fingerprint, at: new Date() };
      setPill(candidateStatus, "error", "测试失败");
      candidateTestResult.textContent = friendlyError(error);
      candidateTestMeta.textContent = "失败时间 " + state.candidateTest.at.toLocaleString();
      setStatus("error", "候选网关测试失败", friendlyError(error));
    })
      .finally(function () { setBusy(testButton, false); loadDiagnostics(); });
  }

  function handleCoreTest() {
    setBusy(coreTestButton, true, "测试中");
    postJSON(API.coreTest, {}).then(function () { setStatus("ok", "Core 可达", "Core 健康检查已通过。"); })
      .catch(function (error) { setStatus("error", "Core 测试失败", friendlyError(error)); })
      .finally(function () { setBusy(coreTestButton, false); loadDiagnostics(); });
  }

  function clearLabels(payload) {
    var labels = { x_api_key: "x-api-key", newapi_user_id: "NewAPI 用户 ID", newapi_token: "NewAPI Token", bearer_token: "Bearer Token" };
    return Object.keys(payload.clear_credentials).filter(function (name) { return payload.clear_credentials[name]; })
      .map(function (name) { return labels[name] || name; });
  }

  function openClearConfirmation(labels) {
    clearConfirmList.textContent = "";
    labels.forEach(function (label) { var item = document.createElement("li"); item.textContent = label; clearConfirmList.appendChild(item); });
    if (typeof clearConfirmDialog.showModal === "function") {
      clearConfirmDialog.showModal();
      window.setTimeout(function () { confirmClearButton.focus(); }, 0);
    }
    else if (window.confirm("确认删除已保存的凭据：" + labels.join("、") + "？")) { performSave(true); }
  }

  function performSave(confirmedClear) {
    var payload = collectPayload();
    var labels = clearLabels(payload);
    var fingerprint = candidateFingerprint();
    var currentTestPassed = state.candidateTest && state.candidateTest.ok && state.candidateTest.fingerprint === fingerprint;
    var clearOnlyChange = labels.length && candidateFingerprintWithoutClears() === state.savedFingerprint;
    var errors = validatePayload(payload, labels.length > 0);
    showValidation(errors);
    if (errors.length) { setStatus("error", "候选配置不完整", "请修正表单中标出的字段后重试。"); formError.focus(); return; }
    if (labels.length && !clearOnlyChange) {
      setStatus("warn", "请先处理其他更改", "取消删除凭据，测试并保存其他更改后，再单独删除凭据。");
      refreshUI();
      return;
    }
    if (!labels.length && !currentTestPassed) {
      setStatus("warn", "请先测试当前配置", "测试通过后才能保存并应用。");
      refreshUI();
      return;
    }
    if (labels.length && !confirmedClear) { openClearConfirmation(labels); return; }
    setBusy(saveButton, true, "保存中");
    postJSON(API.config, payload).then(function (data) {
      [xApiKey, newapiUser, newapiToken, bearerToken].forEach(function (input) { input.value = ""; resetSecretVisibility(input); });
      resetCredentialControls();
      applyConfig(data.config);
      setStatus(data.config.gateway_configured ? "ok" : "warn", "已保存并应用", data.config.gateway_configured ? "候选配置已写入本地磁盘并应用到 Connector。" : "配置已保存，但仍缺少运行所需字段。" );
      updatePreview();
      loadDiagnostics();
    }).catch(function (error) { setStatus("error", "保存失败", friendlyError(error)); })
      .finally(function () { setBusy(saveButton, false); });
  }

  function handleSave(event) { event.preventDefault(); performSave(false); }

  function discardChanges() {
    if (!state.savedConfig) { return; }
    [xApiKey, newapiUser, newapiToken, bearerToken].forEach(function (input) { input.value = ""; resetSecretVisibility(input); });
    resetCredentialControls();
    applyConfig(state.savedConfig);
    clearValidation();
    setStatus("idle", "已放弃候选更改", "表单已恢复为磁盘中的已保存配置。");
  }

  gatewayKind.addEventListener("change", function () { updateGatewayKind(); clearValidation(); });
  secretToggles.forEach(function (button) { button.addEventListener("click", function () { toggleSecretVisibility(button); }); });
  [gatewayUrl, requestTimeout, xApiKey, newapiUser, newapiToken, bearerToken,
    clearXApiKey, clearNewapiUser, clearNewapiToken, clearBearerToken].concat(logLevelInputs).forEach(function (input) {
    input.addEventListener("input", function () { updatePreview(); clearValidation(); refreshUI(); });
    input.addEventListener("change", function () { updatePreview(); clearValidation(); refreshUI(); });
  });
  [[xApiKey, clearXApiKey], [newapiUser, clearNewapiUser], [newapiToken, clearNewapiToken], [bearerToken, clearBearerToken]].forEach(function (pair) {
    pair[0].addEventListener("input", function () {
      if (pair[0].value.trim() && pair[1].checked) { pair[1].checked = false; pair[0].disabled = false; }
    });
    pair[1].addEventListener("change", function () {
      if (pair[1].checked) { pair[0].value = ""; resetSecretVisibility(pair[0]); }
      pair[0].disabled = pair[1].checked;
      var toggle = document.querySelector('[data-secret-toggle="' + pair[0].id + '"]');
      if (toggle) { toggle.disabled = pair[1].checked; }
      refreshUI();
    });
  });
  testButton.addEventListener("click", handleGatewayTest);
  coreTestButton.addEventListener("click", handleCoreTest);
  form.addEventListener("submit", handleSave);
  discardButton.addEventListener("click", discardChanges);
  cancelClearButton.addEventListener("click", function () { clearConfirmDialog.close(); saveButton.focus(); });
  confirmClearButton.addEventListener("click", function () { clearConfirmDialog.close(); performSave(true); });
  clearConfirmDialog.addEventListener("cancel", function () { saveButton.focus(); });
  clearConfirmDialog.addEventListener("click", function (event) {
    if (event.target === clearConfirmDialog) { clearConfirmDialog.close(); saveButton.focus(); }
  });
  window.addEventListener("beforeunload", function (event) {
    if (!currentDirty()) { return; }
    event.preventDefault();
    event.returnValue = "";
  });
  authRetryButton.addEventListener("click", function () {
    if (authGate.dataset.mode === "session-error") { window.location.reload(); return; }
    loadConfig();
  });

  updateGatewayKind();
  refreshUI();
  loadConfig();
}());
