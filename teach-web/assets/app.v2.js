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
    profileRequest: 0,
  };

  const elements = {
    directoryView: document.getElementById("directory-view"),
    profileView: document.getElementById("profile-view"),
    profileLoading: document.getElementById("profile-loading"),
    profileError: document.getElementById("profile-error"),
    profileErrorMessage: document.getElementById("profile-error-message"),
    profilePage: document.getElementById("profile-page"),
    profileContent: document.getElementById("profile-content"),
    profileBreadcrumbName: document.getElementById("profile-breadcrumb-name"),
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
  };

  function node(tag, className, content) {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (content !== undefined && content !== null) element.textContent = String(content);
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
      // The HTTP status below is more useful than a JSON parse error.
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
      if (Number.isFinite(Number(meta.record_count)) && elements.heroCount) {
        elements.heroCount.textContent = formatNumber(meta.record_count);
      }
      const generated = formatDate(meta.generated_at);
      if (elements.datasetUpdated) {
        elements.datasetUpdated.textContent = generated ? `数据更新于 ${generated}` : "数据更新中";
      }
    } catch (_error) {
      if (elements.datasetUpdated) elements.datasetUpdated.textContent = "数据更新中";
    }
  }

  async function loadTeachers() {
    const requestID = ++state.listRequest;
    if (state.listController) state.listController.abort();
    state.listController = new AbortController();
    const params = new URLSearchParams({ page: String(state.page), page_size: String(state.pageSize) });
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
    for (const teacher of teachers) elements.teacherGrid.appendChild(renderTeacherCard(teacher));
  }

  function renderTeacherCard(teacher) {
    const article = node("article", "teacher-card");
    const heading = node("div", "card-heading");
    heading.appendChild(renderAvatar(teacher, "teacher-avatar"));

    const title = node("div", "card-title");
    const titleHeading = node("h3");
    const titleLink = teacherPageLink(teacher, teacher.name || "未命名教师", "teacher-name-link");
    titleHeading.appendChild(titleLink);
    title.appendChild(titleHeading);
    title.appendChild(node("p", "", [teacher.position, teacher.primary_role, teacher.supervisor_roles].filter(Boolean).join(" · ") || "教师"));

    const detail = teacherPageLink(teacher, "查看主页", "detail-button");
    heading.append(title, detail);
    article.appendChild(heading);

    const facts = node("dl", "teacher-facts");
    appendFact(facts, "所在单位", teacher.college);
    appendFact(facts, "任职状态", teacher.employment_status);
    appendFact(facts, "学历", teacher.education || teacher.degree);
    appendFact(facts, "毕业院校", teacher.university);
    article.appendChild(facts);

    if (teacher.introduction) article.appendChild(node("p", "teacher-description", teacher.introduction));

    const bottom = node("div", "card-bottom");
    const research = node("div", "research-list");
    const directions = Array.isArray(teacher.research_directions) ? teacher.research_directions : [];
    for (const direction of directions.slice(0, 2)) research.appendChild(node("span", "research-tag", direction));
    if (directions.length > 2) research.appendChild(node("span", "research-tag", `+${directions.length - 2}`));
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

  function teacherPageLink(teacher, label, className) {
    const link = node("a", className, label);
    if (teacher && teacher.id) link.href = `/teachers/${encodeURIComponent(teacher.id)}`;
    else link.href = "/#teachers";
    return link;
  }

  function renderAvatar(teacher, className) {
    const wrapper = node("span", `${className}-frame`);
    const fallback = node("span", `${className}-fallback`, firstCharacter(teacher && (teacher.initial || teacher.name)));
    wrapper.appendChild(fallback);
    const imageURL = safeImageURL(teacher && teacher.avatar_url);
    if (imageURL) {
      const image = document.createElement("img");
      image.className = className;
      image.src = imageURL;
      image.alt = `${teacher.name || "教师"}的照片`;
      image.loading = "lazy";
      image.decoding = "async";
      image.referrerPolicy = "no-referrer";
      image.addEventListener("error", () => {
        image.remove();
        wrapper.classList.add("is-fallback");
      }, { once: true });
      wrapper.appendChild(image);
    } else {
      wrapper.classList.add("is-fallback");
    }
    return wrapper;
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

  async function loadTeacherProfile(id) {
    const requestID = ++state.profileRequest;
    elements.profileLoading.classList.remove("hidden");
    elements.profilePage.classList.add("hidden");
    elements.profileError.classList.add("hidden");
    elements.profileContent.replaceChildren();
    try {
      const payload = await getJSON(`/api/v1/teachers/${encodeURIComponent(id)}`);
      if (requestID !== state.profileRequest) return;
      const teacher = payload && payload.data ? payload.data : payload;
      if (!teacher) throw new Error("not_found");
      renderProfile(teacher);
      elements.profileLoading.classList.add("hidden");
      elements.profilePage.classList.remove("hidden");
      elements.profileBreadcrumbName.textContent = teacher.name || "教师主页";
      document.title = `${teacher.name || "教师主页"} · 扬华师课`;
    } catch (error) {
      if (requestID !== state.profileRequest) return;
      elements.profileLoading.classList.add("hidden");
      elements.profileError.classList.remove("hidden");
      elements.profileErrorMessage.textContent = friendlyError(error);
    }
  }

  function renderProfile(teacher) {
    const content = document.createDocumentFragment();
    const hero = node("section", "profile-hero");
    hero.appendChild(renderAvatar(teacher, "profile-avatar"));
    const headline = node("div", "profile-headline");
    headline.appendChild(node("p", "eyebrow", "教师主页"));
    const title = node("h1", "", teacher.name || "未命名教师");
    title.id = "profile-page-title";
    headline.appendChild(title);
    headline.appendChild(node("p", "profile-position", [teacher.position, teacher.primary_role, teacher.supervisor_roles].filter(Boolean).join(" · ") || "教师"));
    if (teacher.college) headline.appendChild(node("p", "profile-college", teacher.college));
    const actions = node("div", "profile-actions");
    const profileURL = safeExternalURL(teacher.profile_url);
    if (profileURL) {
      const sourceLink = node("a", "secondary-button", "打开源页面 ↗");
      sourceLink.href = profileURL;
      sourceLink.target = "_blank";
      sourceLink.rel = "noopener noreferrer";
      actions.appendChild(sourceLink);
    }
    headline.appendChild(actions);
    hero.appendChild(headline);
    content.appendChild(hero);

    const layout = node("div", "profile-layout");
    const main = node("div", "profile-main");
    if (teacher.introduction) main.appendChild(profileSection("个人简介", teacher.introduction));
    const directions = Array.isArray(teacher.research_directions) ? teacher.research_directions : [];
    if (directions.length) {
      const research = node("section", "profile-section");
      research.appendChild(node("h2", "", "研究方向"));
      const tags = node("div", "profile-research");
      for (const direction of directions) tags.appendChild(node("span", "research-tag", direction));
      research.appendChild(tags);
      main.appendChild(research);
    }
    const sections = Array.isArray(teacher.sections) ? teacher.sections : [];
    for (const section of sections) {
      if (!section || !section.title || !section.content) continue;
      main.appendChild(profileSection(section.title, section.content));
    }
    if (!teacher.introduction && !directions.length && sections.length === 0) {
      main.appendChild(node("p", "profile-empty", "这位教师暂时没有更多资料。"));
    }
    layout.appendChild(main);

    const aside = node("aside", "profile-sidebar");
    const facts = node("dl", "profile-facts");
    appendProfileFact(facts, "所在单位", teacher.college);
    appendProfileFact(facts, "主要任职", teacher.primary_role);
    appendProfileFact(facts, "任职状态", teacher.employment_status);
    appendProfileFact(facts, "入职时间", teacher.entry_date);
    appendProfileFact(facts, "学历", teacher.education);
    appendProfileFact(facts, "学位", teacher.degree);
    appendProfileFact(facts, "毕业院校", teacher.university);
    appendProfileFact(facts, "性别", teacher.gender);
    appendProfileFact(facts, "办公地点", teacher.office_location);
    appendProfileFact(facts, "邮编", teacher.postal_code);
    appendProfileContact(facts, "邮箱", teacher.email, "email");
    appendProfileContact(facts, "联系电话", teacher.phone, "phone");
    if (facts.children.length) aside.appendChild(facts);
    layout.appendChild(aside);
    content.appendChild(layout);
    elements.profileContent.replaceChildren(content);
  }

  function profileSection(title, content) {
    const section = node("section", "profile-section");
    section.append(node("h2", "", title), node("p", "profile-section-content", content));
    return section;
  }

  function appendProfileFact(container, label, value) {
    if (!value) return;
    const wrapper = node("div", "profile-fact");
    wrapper.append(node("dt", "", label), node("dd", "", value));
    container.appendChild(wrapper);
  }

  function appendProfileContact(container, label, value, kind) {
    if (!value) return;
    const wrapper = node("div", "profile-fact");
    wrapper.appendChild(node("dt", "", label));
    const valueNode = node("dd");
    if (kind === "email" && /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value)) {
      const link = node("a", "contact-link", value);
      link.href = `mailto:${value}`;
      valueNode.appendChild(link);
    } else if (kind === "phone" && /^[+()\-\s\d]+$/.test(value)) {
      const link = node("a", "contact-link", value);
      link.href = `tel:${value.replace(/[^+\d]/g, "")}`;
      valueNode.appendChild(link);
    } else {
      valueNode.textContent = value;
    }
    wrapper.appendChild(valueNode);
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

  function showDirectory() {
    elements.directoryView.classList.remove("hidden");
    elements.profileView.classList.add("hidden");
    document.title = "扬华师课 · 教师与开课信息";
  }

  function showProfile() {
    elements.directoryView.classList.add("hidden");
    elements.profileView.classList.remove("hidden");
  }

  function route() {
    const match = window.location.pathname.match(/^\/teachers\/([^/]+)\/?$/);
    if (match) {
      showProfile();
      loadTeacherProfile(decodeURIComponent(match[1]));
      return;
    }
    showDirectory();
    loadMeta();
    loadTeachers();
    if (window.location.hash === "#teachers") {
      window.setTimeout(() => document.getElementById("teachers")?.scrollIntoView({ block: "start" }), 0);
    }
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

  function safeImageURL(value) {
    const external = safeExternalURL(value);
    if (!external) return "";
    try {
      const parsed = new URL(external);
      if (window.location.protocol === "https:" && parsed.protocol === "http:" && parsed.hostname.toLowerCase() === "faculty.swjtu.edu.cn") {
        parsed.protocol = "https:";
      }
      return parsed.href;
    } catch (_error) {
      return "";
    }
  }

  function friendlyError(error) {
    if (!error) return "网络连接失败，请稍后重试。";
    if (error.message === "Failed to fetch") return "无法连接到数据服务，请稍后重试。";
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
  window.addEventListener("popstate", route);
  route();
})();
