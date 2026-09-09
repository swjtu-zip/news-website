(() => {
  "use strict";

  const apiMeta = document.querySelector('meta[name="api-base"]');
  const configuredBase = window.TEACH_API_BASE || (apiMeta && apiMeta.content) || "/api";
  const API_BASE = String(configuredBase).replace(/\/+$/, "");
  const state = {
    page: 1,
    pageSize: 24,
    search: "",
    initial: "",
    totalPages: 0,
    total: 0,
    listRequest: 0,
    listController: null,
  };

  const elements = {
    heroCount: document.getElementById("hero-count"),
    datasetUpdated: document.getElementById("dataset-updated"),
    filterForm: document.getElementById("filter-form"),
    searchInput: document.getElementById("search-input"),
    initialSelect: document.getElementById("initial-select"),
    resetButton: document.getElementById("reset-button"),
    emptyReset: document.getElementById("empty-reset"),
    retryButton: document.getElementById("retry-button"),
    listStatus: document.getElementById("list-status"),
    teacherGrid: document.getElementById("teacher-grid"),
    emptyState: document.getElementById("empty-state"),
    errorState: document.getElementById("error-state"),
    errorMessage: document.getElementById("error-message"),
    pagination: document.getElementById("pagination"),
    previousPage: document.getElementById("previous-page"),
    nextPage: document.getElementById("next-page"),
    pageLabel: document.getElementById("page-label"),
    dialog: document.getElementById("teacher-dialog"),
    dialogClose: document.getElementById("dialog-close"),
    dialogContent: document.getElementById("dialog-content"),
  };

  function node(tag, className, content) {
    const element = document.createElement(tag);
    if (className) {
      element.className = className;
    }
    if (content !== undefined && content !== null) {
      element.textContent = String(content);
    }
    return element;
  }

  function apiURL(path) {
    // API_BASE may be an origin (http://localhost:8080) or the same-origin
    // public prefix (/api). Do not duplicate the prefix in the latter case.
    const relativePath = API_BASE.endsWith("/api") && path.startsWith("/api/")
      ? path.slice(4)
      : path;
    return `${API_BASE}${relativePath}`;
  }

  async function getJSON(path, signal) {
    const response = await fetch(apiURL(path), {
      method: "GET",
      headers: { Accept: "application/json" },
      cache: "default",
      signal,
    });
    let payload = null;
    try {
      payload = await response.json();
    } catch (_error) {
      // The status below is more useful than a JSON parse error for operators.
    }
    if (!response.ok) {
      const error = payload && payload.error ? payload.error : `HTTP ${response.status}`;
      throw new Error(error);
    }
    return payload;
  }

  async function loadMeta() {
    try {
      const payload = await getJSON("/api/v1/meta");
      const meta = payload && payload.data ? payload.data : payload;
      if (!meta) return;
      if (Number.isFinite(Number(meta.record_count))) {
        elements.heroCount.textContent = formatNumber(meta.record_count);
      }
      const generated = formatDate(meta.generated_at);
      elements.datasetUpdated.textContent = generated ? `数据快照 · ${generated}` : "公开数据快照";
    } catch (_error) {
      elements.datasetUpdated.textContent = "公开数据快照";
    }
  }

  async function loadTeachers() {
    const requestID = ++state.listRequest;
    if (state.listController) {
      state.listController.abort();
    }
    state.listController = new AbortController();
    const params = new URLSearchParams({
      page: String(state.page),
      page_size: String(state.pageSize),
    });
    if (state.search) params.set("search", state.search);
    if (state.initial) params.set("initial", state.initial);

    setLoading(true);
    try {
      const payload = await getJSON(`/api/v1/teachers?${params.toString()}`, state.listController.signal);
      if (requestID !== state.listRequest) return;
      const page = payload || { data: [], pagination: {} };
      const pagination = page.pagination || {};
      state.total = Number(pagination.total) || 0;
      state.totalPages = Number(pagination.total_pages) || 0;
      renderTeachers(Array.isArray(page.data) ? page.data : []);
      renderPagination();
      if (state.total > 0) {
        elements.heroCount.textContent = formatNumber(state.total);
        elements.listStatus.textContent = `共 ${formatNumber(state.total)} 位教师 · 第 ${state.page} / ${state.totalPages} 页`;
      } else {
        elements.listStatus.textContent = "当前筛选条件下没有结果";
      }
    } catch (error) {
      if (error && error.name === "AbortError") return;
      if (requestID !== state.listRequest) return;
      elements.teacherGrid.replaceChildren();
      elements.pagination.classList.add("hidden");
      elements.emptyState.classList.add("hidden");
      elements.errorState.classList.remove("hidden");
      elements.errorMessage.textContent = friendlyError(error);
      elements.listStatus.textContent = "加载失败";
    } finally {
      if (requestID === state.listRequest) setLoading(false);
    }
  }

  function renderTeachers(teachers) {
    elements.errorState.classList.add("hidden");
    elements.emptyState.classList.toggle("hidden", teachers.length !== 0);
    elements.teacherGrid.replaceChildren();
    for (const teacher of teachers) {
      elements.teacherGrid.appendChild(renderTeacherCard(teacher));
    }
  }

  function renderTeacherCard(teacher) {
    const article = node("article", "teacher-card");
    const heading = node("div", "card-heading");
    const initial = node("span", "teacher-initial", firstCharacter(teacher.initial || teacher.name));
    const title = node("div", "card-title");
    title.appendChild(node("h3", "", teacher.name || "未命名教师"));
    title.appendChild(node("p", "", [teacher.position, teacher.supervisor_roles].filter(Boolean).join(" · ") || "教师"));
    const detail = node("button", "detail-button", "查看档案");
    detail.type = "button";
    detail.addEventListener("click", () => openTeacher(teacher.id));
    heading.append(initial, title, detail);
    article.appendChild(heading);

    const facts = node("dl", "teacher-facts");
    appendFact(facts, "所在单位", teacher.college);
    appendFact(facts, "任职状态", teacher.employment_status);
    appendFact(facts, "学历", teacher.education || teacher.degree);
    appendFact(facts, "毕业院校", teacher.university);
    article.appendChild(facts);

    if (teacher.introduction) {
      article.appendChild(node("p", "teacher-description", teacher.introduction));
    }

    const bottom = node("div", "card-bottom");
    const research = node("div", "research-list");
    const directions = Array.isArray(teacher.research_directions) ? teacher.research_directions : [];
    for (const direction of directions.slice(0, 2)) {
      research.appendChild(node("span", "research-tag", direction));
    }
    if (directions.length > 2) {
      research.appendChild(node("span", "research-tag", `+${directions.length - 2}`));
    }
    bottom.appendChild(research);
    const profileURL = safeExternalURL(teacher.profile_url);
    if (profileURL) {
      const link = node("a", "profile-link", "源页面 ↗");
      link.href = profileURL;
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      bottom.appendChild(link);
    }
    article.appendChild(bottom);
    return article;
  }

  function appendFact(container, label, value) {
    if (!value) return;
    const wrapper = node("div", "teacher-fact");
    wrapper.append(node("dt", "", label), node("dd", "", value));
    container.appendChild(wrapper);
  }

  function renderPagination() {
    const hasPages = state.totalPages > 1;
    elements.pagination.classList.toggle("hidden", !hasPages);
    elements.previousPage.disabled = state.page <= 1;
    elements.nextPage.disabled = state.page >= state.totalPages;
    elements.pageLabel.textContent = `第 ${state.page} / ${state.totalPages || 1} 页`;
  }

  async function openTeacher(id) {
    if (!id) return;
    elements.dialogContent.replaceChildren(node("p", "dialog-loading", "正在读取资料…"));
    if (typeof elements.dialog.showModal === "function") {
      elements.dialog.showModal();
    } else {
      elements.dialog.setAttribute("open", "");
    }
    try {
      const payload = await getJSON(`/api/v1/teachers/${encodeURIComponent(id)}`);
      renderTeacherDetail(payload && payload.data ? payload.data : payload);
    } catch (error) {
      elements.dialogContent.replaceChildren(
        node("p", "dialog-loading", `资料加载失败：${friendlyError(error)}`),
      );
    }
  }

  function renderTeacherDetail(teacher) {
    if (!teacher) return;
    const content = document.createDocumentFragment();
    const heading = node("div", "dialog-profile-heading");
    heading.appendChild(node("span", "teacher-initial", firstCharacter(teacher.initial || teacher.name)));
    const title = node("div");
    title.appendChild(node("h2", "", teacher.name || "未命名教师"));
    title.appendChild(node("p", "", [teacher.position, teacher.supervisor_roles].filter(Boolean).join(" · ") || "教师"));
    heading.appendChild(title);
    content.appendChild(heading);

    const facts = node("dl", "dialog-facts");
    appendDialogFact(facts, "所在单位", teacher.college);
    appendDialogFact(facts, "任职状态", teacher.employment_status);
    appendDialogFact(facts, "学历 / 学位", [teacher.education, teacher.degree].filter(Boolean).join(" · "));
    appendDialogFact(facts, "毕业院校", teacher.university);
    appendDialogFact(facts, "性别", teacher.gender);
    content.appendChild(facts);

    if (teacher.introduction) {
      const section = node("section", "dialog-section");
      section.append(node("h3", "", "个人简介"), node("p", "", teacher.introduction));
      content.appendChild(section);
    }
    const directions = Array.isArray(teacher.research_directions) ? teacher.research_directions : [];
    if (directions.length) {
      const section = node("section", "dialog-section");
      const tags = node("div", "dialog-research");
      for (const direction of directions) tags.appendChild(node("span", "research-tag", direction));
      section.append(node("h3", "", "研究方向"), tags);
      content.appendChild(section);
    }
    const profileURL = safeExternalURL(teacher.profile_url);
    if (profileURL) {
      const link = node("a", "dialog-source-link", "打开教师源页面 ↗");
      link.href = profileURL;
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      content.appendChild(link);
    }
    elements.dialogContent.replaceChildren(content);
  }

  function appendDialogFact(container, label, value) {
    if (!value) return;
    const wrapper = node("div", "dialog-fact");
    wrapper.append(node("dt", "", label), node("dd", "", value));
    container.appendChild(wrapper);
  }

  function setLoading(loading) {
    elements.listStatus.classList.toggle("is-loading", loading);
    if (loading) {
      elements.listStatus.textContent = "正在读取教师目录…";
      elements.emptyState.classList.add("hidden");
      elements.errorState.classList.add("hidden");
    }
  }

  function resetFilters() {
    state.page = 1;
    state.search = "";
    state.initial = "";
    elements.searchInput.value = "";
    elements.initialSelect.value = "";
    loadTeachers();
  }

  function formatNumber(value) {
    return Number(value || 0).toLocaleString("zh-CN");
  }

  function formatDate(value) {
    if (!value) return "";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "";
    return new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "numeric", day: "numeric" }).format(date);
  }

  function firstCharacter(value) {
    const text = String(value || "?").trim();
    return (text[0] || "?").toUpperCase();
  }

  function safeExternalURL(value) {
    try {
      const parsed = new URL(value);
      return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.href : "";
    } catch (_error) {
      return "";
    }
  }

  function friendlyError(error) {
    if (!error) return "网络连接失败，请稍后重试。";
    if (error.message === "Failed to fetch") return "无法连接到数据服务，请确认 API 已启动。";
    if (error.message === "not_found") return "这条教师资料已不存在。";
    return "网络连接失败，请稍后重试。";
  }

  elements.filterForm.addEventListener("submit", (event) => {
    event.preventDefault();
    state.page = 1;
    state.search = elements.searchInput.value.trim();
    state.initial = elements.initialSelect.value;
    loadTeachers();
  });
  elements.searchInput.addEventListener("input", () => {
    window.clearTimeout(elements.searchInput.searchTimer);
    elements.searchInput.searchTimer = window.setTimeout(() => {
      state.page = 1;
      state.search = elements.searchInput.value.trim();
      loadTeachers();
    }, 280);
  });
  elements.initialSelect.addEventListener("change", () => {
    state.page = 1;
    state.initial = elements.initialSelect.value;
    loadTeachers();
  });
  elements.resetButton.addEventListener("click", resetFilters);
  elements.emptyReset.addEventListener("click", resetFilters);
  elements.retryButton.addEventListener("click", loadTeachers);
  elements.previousPage.addEventListener("click", () => {
    if (state.page > 1) {
      state.page -= 1;
      loadTeachers();
      document.getElementById("teachers").scrollIntoView({ block: "start" });
    }
  });
  elements.nextPage.addEventListener("click", () => {
    if (state.page < state.totalPages) {
      state.page += 1;
      loadTeachers();
      document.getElementById("teachers").scrollIntoView({ block: "start" });
    }
  });
  elements.dialogClose.addEventListener("click", () => elements.dialog.close());
  elements.dialog.addEventListener("click", (event) => {
    if (event.target === elements.dialog) elements.dialog.close();
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "/" && document.activeElement !== elements.searchInput && !elements.dialog.open) {
      event.preventDefault();
      elements.searchInput.focus();
    }
  });

  loadMeta();
  loadTeachers();
})();
