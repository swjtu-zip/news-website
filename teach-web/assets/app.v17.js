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
    coursePage: 1,
    coursePageSize: 24,
    courseSearch: "",
    courseCampus: "",
    courseWeekday: "",
    courseCategory: "",
    courseTotalPages: 0,
    courseTotal: 0,
    courseLoaded: 0,
    courseLoading: false,
    courseListRequest: 0,
    courseListController: null,
    courseRequest: 0,
  };

  const elements = {
    homeView: document.getElementById("home-view"),
    teachersView: document.getElementById("teachers-view"),
    profileView: document.getElementById("profile-view"),
    coursesView: document.getElementById("courses-view"),
    courseView: document.getElementById("course-view"),
    navHome: document.getElementById("nav-home"),
    navTeachers: document.getElementById("nav-teachers"),
    navCourses: document.getElementById("nav-courses"),
    profileLoading: document.getElementById("profile-loading"),
    profileError: document.getElementById("profile-error"),
    profileErrorMessage: document.getElementById("profile-error-message"),
    profilePage: document.getElementById("profile-page"),
    profileContent: document.getElementById("profile-content"),
    profileBreadcrumbName: document.getElementById("profile-breadcrumb-name"),
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
    coursesTerm: document.getElementById("courses-term"),
    courseFilterForm: document.getElementById("course-filter-form"),
    courseSearchInput: document.getElementById("course-search-input"),
    courseCampusSelect: document.getElementById("course-campus-select"),
    courseWeekdaySelect: document.getElementById("course-weekday-select"),
    courseCategorySelect: document.getElementById("course-category-select"),
    courseResetButton: document.getElementById("course-reset-button"),
    courseEmptyReset: document.getElementById("course-empty-reset"),
    courseRetryButton: document.getElementById("course-retry-button"),
    courseListStatus: document.getElementById("course-list-status"),
    courseList: document.getElementById("course-list"),
    courseEmptyState: document.getElementById("course-empty-state"),
    courseErrorState: document.getElementById("course-error-state"),
    courseErrorMessage: document.getElementById("course-error-message"),
    courseSentinel: document.getElementById("course-sentinel"),
    courseLoading: document.getElementById("course-loading"),
    courseError: document.getElementById("course-error"),
    courseErrorMessageDetail: document.getElementById("course-error-message-detail"),
    coursePage: document.getElementById("course-page"),
    courseContent: document.getElementById("course-content"),
    courseBreadcrumbName: document.getElementById("course-breadcrumb-name"),
    homeTeacherCount: document.getElementById("home-teacher-count"),
    homeCourseBadge: document.getElementById("home-course-badge"),
    homeCourseCount: document.getElementById("home-course-count"),
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
      if (Number.isFinite(Number(meta.record_count))) {
        if (elements.homeTeacherCount) elements.homeTeacherCount.textContent = formatNumber(meta.record_count);
      }
      const generated = formatDate(meta.generated_at);
      if (elements.datasetUpdated) {
        elements.datasetUpdated.textContent = generated ? `更新时间 ${generated}` : "更新时间读取中";
      }
      if (Number.isFinite(Number(meta.course_count)) && Number(meta.course_count) > 0) {
        if (elements.homeCourseCount) elements.homeCourseCount.textContent = formatNumber(meta.course_count);
        if (elements.coursesTerm && meta.courses_term) elements.coursesTerm.textContent = meta.courses_term;
        if (elements.homeCourseBadge && meta.courses_term) elements.homeCourseBadge.textContent = meta.courses_term;
      }
    } catch (_error) {
      if (elements.datasetUpdated) elements.datasetUpdated.textContent = "更新时间读取中";
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
    else link.href = "/teachers";
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

  // 课程列表采用无限滚动:首屏替换整列,触底时追加下一页。
  async function loadCourses(append = false) {
    if (append) {
      if (state.courseLoading) return;
      if (state.courseTotalPages === 0 || state.coursePage >= state.courseTotalPages) return;
    }
    const requestID = ++state.courseListRequest;
    if (!append && state.courseListController) state.courseListController.abort();
    const controller = new AbortController();
    state.courseListController = controller;
    const targetPage = append ? state.coursePage + 1 : 1;
    const params = new URLSearchParams({ page: String(targetPage), page_size: String(state.coursePageSize) });
    if (state.courseSearch) params.set("search", state.courseSearch);
    if (state.courseCampus) params.set("campus", state.courseCampus);
    if (state.courseWeekday) params.set("weekday", state.courseWeekday);
    if (state.courseCategory) params.set("category", state.courseCategory);

    state.courseLoading = true;
    setCourseLoading(!append);
    updateCourseSentinel();
    try {
      const payload = await getJSON(`/api/v1/courses?${params.toString()}`, controller.signal);
      if (requestID !== state.courseListRequest) return;
      const page = payload || { data: [], pagination: {} };
      const pagination = page.pagination || {};
      state.coursePage = targetPage;
      state.courseTotal = Number(pagination.total) || 0;
      state.courseTotalPages = Number(pagination.total_pages) || 0;
      const courses = Array.isArray(page.data) ? page.data : [];
      renderCourses(courses, append);
      if (state.courseTotal > 0) {
        elements.courseListStatus.textContent = `已加载 ${formatNumber(state.courseLoaded)} / 共 ${formatNumber(state.courseTotal)} 门课`;
      } else {
        elements.courseListStatus.textContent = "当前筛选条件下没有结果";
      }
    } catch (error) {
      if (error && error.name === "AbortError") return;
      if (requestID !== state.courseListRequest) return;
      elements.courseList.replaceChildren();
      state.courseLoaded = 0;
      elements.courseEmptyState.classList.add("hidden");
      elements.courseErrorState.classList.remove("hidden");
      elements.courseErrorMessage.textContent = friendlyError(error);
      elements.courseListStatus.textContent = "加载失败";
    } finally {
      if (requestID === state.courseListRequest) {
        state.courseLoading = false;
        setCourseLoading(false);
        updateCourseSentinel();
      }
    }
  }

  function setCourseLoading(loading) {
    elements.courseListStatus.classList.toggle("is-loading", loading);
    if (loading) {
      elements.courseListStatus.textContent = "正在读取开课信息…";
      elements.courseEmptyState.classList.add("hidden");
      elements.courseErrorState.classList.add("hidden");
    }
  }

  function renderCourses(courses, append = false) {
    elements.courseErrorState.classList.add("hidden");
    if (!append) {
      elements.courseList.replaceChildren();
      state.courseLoaded = 0;
    }
    for (const course of courses) elements.courseList.appendChild(renderCourseCard(course));
    state.courseLoaded += courses.length;
    elements.courseEmptyState.classList.toggle("hidden", state.courseLoaded !== 0);
  }

  // anchorClass 为 true 时(教师主页的开课列表),链接带上 ?class=教学班号:
  // 课程页会把该班的大卡片和它的活水评价置顶展示。
  function renderCourseCard(course, anchorClass = false) {
    const article = node("article", "course-row");
    // 课程页以课程代码为键:同一门课的全部教学班聚合在一个页面,链接跨学期稳定。
    let courseURL = `/courses/${encodeURIComponent(course.course_code || course.id)}`;
    if (anchorClass && course.course_code && course.id) {
      courseURL += `?class=${encodeURIComponent(course.id)}`;
    }
    article.tabIndex = 0;
    article.setAttribute("role", "link");
    article.setAttribute("aria-label", `查看课程详情:${course.course_name || "未命名课程"}`);

    const main = node("div", "course-row-main");
    const title = node("h3");
    const link = node("a", "course-name-link", course.course_name || "未命名课程");
    link.href = courseURL;
    title.appendChild(link);
    main.appendChild(title);
    const meta = [course.course_code, course.college, course.credit ? `${course.credit} 学分` : ""].filter(Boolean).join(" · ");
    main.appendChild(node("p", "course-row-meta", meta));
    // 课程性质(必修/限选/选修)单独一个标签;分类里混着"XX2026-01班"这类
    // 选课班级名单,概览里只展示真正的分类。
    const tags = node("div", "course-row-categories");
    if (course.nature) tags.appendChild(node("span", "course-chip course-chip-nature", course.nature));
    const categories = splitCourseCategories(course.category).filter((token) => !token.includes("班"));
    for (const category of categories.slice(0, 3)) tags.appendChild(node("span", "course-chip", category));
    if (tags.childNodes.length) main.appendChild(tags);
    article.appendChild(main);

    const schedule = node("div", "course-row-schedule");
    for (const value of [course.campus, course.weekday, course.periods ? `${course.periods} 节` : ""].filter(Boolean)) {
      schedule.appendChild(node("span", "course-chip", value));
    }
    article.appendChild(schedule);

    if (course.huoshui) {
      // 徽标可点:直接进到该班的焦点视图(大卡片 + 活水评价置顶)。
      article.appendChild(renderHuoshuiMiniBadge(course.huoshui, () => {
        window.location.assign(`/courses/${encodeURIComponent(course.course_code || course.id)}?class=${encodeURIComponent(course.id)}`);
      }));
    }

    const teacher = node("div", "course-row-teacher");
    teacher.appendChild(node("span", "course-teacher-label", "授课教师"));
    if (course.teacher_id) {
      const teacherLink = node("a", "course-teacher-link", course.teacher_name || "未命名");
      teacherLink.href = `/teachers/${encodeURIComponent(course.teacher_id)}`;
      teacher.appendChild(teacherLink);
    } else {
      teacher.appendChild(node("span", "course-teacher-plain", course.teacher_name || "未命名"));
    }
    article.appendChild(teacher);

    article.addEventListener("click", (event) => {
      // 行内的教师链接与评分徽标保持自己的跳转,其余区域都进入课程详情。
      if (event.target.closest("a") || event.target.closest(".huoshui-mini-badge")) return;
      if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
        window.open(courseURL, "_blank", "noopener");
      } else {
        window.location.assign(courseURL);
      }
    });
    article.addEventListener("keydown", (event) => {
      if (event.target !== article) return;
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        window.location.assign(courseURL);
      }
    });
    return article;
  }

  // 哨兵节点提示加载进度;滚动接近底部时由 IntersectionObserver 触发追加加载。
  function updateCourseSentinel() {
    if (state.courseTotal === 0) {
      elements.courseSentinel.classList.add("hidden");
      return;
    }
    elements.courseSentinel.classList.remove("hidden");
    if (state.courseLoading) {
      elements.courseSentinel.textContent = "正在读取更多课程…";
    } else if (state.coursePage < state.courseTotalPages) {
      elements.courseSentinel.textContent = "滚动加载更多…";
    } else {
      elements.courseSentinel.textContent = "已加载全部课程";
    }
  }

  function resetCourseFilters() {
    state.coursePage = 1;
    state.courseSearch = "";
    state.courseCampus = "";
    state.courseWeekday = "";
    state.courseCategory = "";
    elements.courseSearchInput.value = "";
    elements.courseCampusSelect.value = "";
    elements.courseWeekdaySelect.value = "";
    elements.courseCategorySelect.value = "";
    loadCourses();
  }

  async function loadCourseCategories() {
    try {
      const payload = await getJSON("/api/v1/courses/categories");
      const categories = payload && Array.isArray(payload.data) ? payload.data : [];
      const select = elements.courseCategorySelect;
      select.replaceChildren(node("option", "", "全部分类"));
      select.firstChild.value = "";
      for (const category of categories) {
        const option = node("option", "", category);
        option.value = category;
        select.appendChild(option);
      }
      select.value = state.courseCategory;
    } catch (_error) {
      // 分类列表不可用时保留"全部分类",不影响课程列表本身。
    }
  }

  async function loadCourseDetail(param, pinnedClass = "") {
    const requestID = ++state.courseRequest;
    elements.courseLoading.classList.remove("hidden");
    elements.coursePage.classList.add("hidden");
    elements.courseError.classList.add("hidden");
    elements.courseContent.replaceChildren();
    try {
      let payload = null;
      // 教学班号是 B+四位数字(体育班带 -N 后缀),课程代码更长;
      // 先按形态猜一次,猜错再回退另一个端点,避免每次详情都发两个请求。
      const looksLikeTeachId = /^B\d{4}(-\d+)?$/i.test(param);
      if (!looksLikeTeachId) {
        try {
          payload = await getJSON(`/api/v1/courses/code/${encodeURIComponent(param)}`);
        } catch (_codeError) {
          payload = null;
        }
      }
      if (!payload) {
        // 旧链接用的是教学班号:解析出课程代码后原地换成规范的课程页地址,
        // 并把该班置为焦点班(等价于 ?class=教学班号)。
        const legacy = await getJSON(`/api/v1/courses/${encodeURIComponent(param)}`);
        const legacyCourse = legacy && legacy.data ? legacy.data : legacy;
        if (!legacyCourse || !legacyCourse.course_code) throw new Error("not_found");
        if (!pinnedClass) pinnedClass = legacyCourse.id || "";
        const classQuery = pinnedClass ? `?class=${encodeURIComponent(pinnedClass)}` : "";
        window.history.replaceState({}, "", `/courses/${encodeURIComponent(legacyCourse.course_code)}${classQuery}`);
        payload = await getJSON(`/api/v1/courses/code/${encodeURIComponent(legacyCourse.course_code)}`);
      }
      if (requestID !== state.courseRequest) return;
      const group = payload && payload.data ? payload.data : payload;
      if (!group || !Array.isArray(group.classes) || !group.classes.length) throw new Error("not_found");
      renderCourseDetail(group, requestID, pinnedClass);
      elements.courseLoading.classList.add("hidden");
      elements.coursePage.classList.remove("hidden");
      elements.courseBreadcrumbName.textContent = group.course_name || "课程详情";
      document.title = `${group.course_name || "课程详情"} · 扬华师课`;
    } catch (error) {
      if (requestID !== state.courseRequest) return;
      elements.courseLoading.classList.add("hidden");
      elements.courseError.classList.remove("hidden");
      elements.courseErrorMessageDetail.textContent = friendlyError(error);
    }
  }

  function renderCourseDetail(group, requestID, pinnedClass = "") {
    const classes = group.classes;
    const pinned = pinnedClass ? classes.find((cls) => cls.id === pinnedClass) : null;
    const content = document.createDocumentFragment();
    const hero = node("section", "course-hero");
    const headline = node("div", "course-headline");
    headline.appendChild(node("p", "eyebrow", "课程详情"));
    const title = node("h1", "", group.course_name || "未命名课程");
    title.id = "course-page-title";
    headline.appendChild(title);
    headline.appendChild(node("p", "course-hero-meta", [
      group.course_code,
      group.college,
      group.credit ? `${group.credit} 学分` : "",
      `共 ${classes.length} 个教学班`,
    ].filter(Boolean).join(" · ")));
    // 汇总全部教学班的性质与分类标签(去重),班级名单类的不展示。
    const tags = node("div", "course-hero-tags");
    const seenTag = new Set();
    for (const cls of classes) {
      const candidates = [cls.nature, ...splitCourseCategories(cls.category).filter((token) => !token.includes("班"))];
      for (const tag of candidates) {
        if (tag && !seenTag.has(tag)) {
          seenTag.add(tag);
          tags.appendChild(node("span", "course-hero-tag", tag));
        }
      }
    }
    if (tags.childNodes.length) headline.appendChild(tags);
    hero.appendChild(headline);
    content.appendChild(hero);

    // 焦点班(从教师主页或评分徽标点进来时)单独置顶一张大卡片,
    // 它的活水评价紧跟卡片之后,不用翻到底部找。
    if (pinned) {
      content.appendChild(renderPinnedClass(pinned));
      const pinnedHuoshuiAnchor = node("div", "");
      content.appendChild(pinnedHuoshuiAnchor);
      if (pinned.huoshui) loadCourseHuoshuiSection(pinned.huoshui, requestID, pinnedHuoshuiAnchor);
    }

    const rest = pinned ? classes.filter((cls) => cls !== pinned) : classes;
    if (rest.length) {
      const restSection = node("section", "profile-section");
      restSection.appendChild(node("h2", "", pinned ? `其他教学班 (${rest.length})` : `全部教学班 (${rest.length})`));
      for (const cls of rest) restSection.appendChild(renderCourseClass(cls));
      content.appendChild(restSection);
    }
    elements.courseContent.replaceChildren(content);

    // 同一门课不同老师可能各自有活水页面,逐个展示评分与评价(焦点班的已在顶部)。
    const seenHuoshui = new Set(pinned && pinned.huoshui ? [pinned.huoshui.objectId] : []);
    for (const cls of classes) {
      if (cls.huoshui && !seenHuoshui.has(cls.huoshui.objectId)) {
        seenHuoshui.add(cls.huoshui.objectId);
        loadCourseHuoshuiSection(cls.huoshui, requestID, elements.courseContent);
      }
    }
  }

  // 焦点班的大卡片:授课教师一栏与评分徽标都在卡片头部,信息一屏看全。
  function renderPinnedClass(cls) {
    const card = node("section", "course-facts-card course-class-card course-class-pinned");
    const head = node("div", "course-class-head");
    const identity = node("div", "course-class-identity");
    identity.appendChild(node("span", "course-teacher-label", "授课教师"));
    identity.appendChild(node("strong", "course-class-teacher", cls.teacher_name || "未命名"));
    if (cls.teacher_title) identity.appendChild(node("span", "course-class-title", cls.teacher_title));
    if (cls.teacher_id) {
      const teacherLink = node("a", "course-teacher-link", "教师主页 →");
      teacherLink.href = `/teachers/${encodeURIComponent(cls.teacher_id)}`;
      identity.appendChild(teacherLink);
    } else {
      identity.appendChild(node("span", "course-teacher-note", "暂未收录教师主页"));
    }
    head.appendChild(identity);
    const chips = node("div", "course-row-schedule");
    for (const value of [cls.term, cls.id ? `班号 ${cls.id}` : "", cls.campus, cls.weekday, cls.periods ? `${cls.periods} 节` : ""].filter(Boolean)) {
      chips.appendChild(node("span", "course-chip", value));
    }
    head.appendChild(chips);
    if (cls.huoshui) {
      head.appendChild(renderHuoshuiMiniBadge(cls.huoshui, () => scrollToHuoshui(cls.huoshui)));
    }
    card.appendChild(head);

    const facts = node("dl", "course-facts");
    appendCourseFact(facts, "教学班", cls.class_num);
    appendCourseFact(facts, "总学时", cls.hours_total ? `${cls.hours_total}` : "");
    appendCourseFact(facts, "周学时", cls.hours_week ? `${cls.hours_week}` : "");
    appendCourseFact(facts, "周次", cls.weeks);
    appendCourseFact(facts, "上课时间", cls.schedule_text);
    appendCourseFact(facts, "考核方式", cls.assessment);
    appendCourseFact(facts, "课程性质", cls.nature);
    const categoryTokens = splitCourseCategories(cls.category);
    const categoryLabel = categoryTokens.length && categoryTokens.every((token) => token.includes("班")) ? "优选班" : "课程分类";
    appendCourseFact(facts, categoryLabel, categoryTokens.join("、"));
    appendCourseFact(facts, "备注", cls.remark);
    card.appendChild(facts);
    return card;
  }

  function scrollToHuoshui(huoshui) {
    const section = document.getElementById(`huoshui-${huoshui.objectId}`);
    if (section) section.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  function renderCourseClass(cls) {
    const card = node("section", "course-facts-card course-class-card");
    const head = node("div", "course-class-head");
    const identity = node("div", "course-class-identity");
    identity.appendChild(node("strong", "course-class-teacher", cls.teacher_name || "未命名"));
    if (cls.teacher_title) identity.appendChild(node("span", "course-class-title", cls.teacher_title));
    if (cls.teacher_id) {
      const teacherLink = node("a", "course-teacher-link", "教师主页 →");
      teacherLink.href = `/teachers/${encodeURIComponent(cls.teacher_id)}`;
      identity.appendChild(teacherLink);
    }
    head.appendChild(identity);
    const chips = node("div", "course-row-schedule");
    for (const value of [cls.term, cls.id ? `班号 ${cls.id}` : "", cls.campus, cls.weekday, cls.periods ? `${cls.periods} 节` : ""].filter(Boolean)) {
      chips.appendChild(node("span", "course-chip", value));
    }
    head.appendChild(chips);
    if (cls.huoshui) head.appendChild(renderHuoshuiMiniBadge(cls.huoshui, () => scrollToHuoshui(cls.huoshui)));
    card.appendChild(head);

    const facts = node("dl", "course-facts");
    appendCourseFact(facts, "教学班", cls.class_num);
    appendCourseFact(facts, "总学时", cls.hours_total ? `${cls.hours_total}` : "");
    appendCourseFact(facts, "周学时", cls.hours_week ? `${cls.hours_week}` : "");
    appendCourseFact(facts, "周次", cls.weeks);
    appendCourseFact(facts, "上课时间", cls.schedule_text);
    appendCourseFact(facts, "考核方式", cls.assessment);
    appendCourseFact(facts, "课程性质", cls.nature);
    // 必修课的分类字段记录的是优选班名单,栏目名跟着内容走。
    const categoryTokens = splitCourseCategories(cls.category);
    const categoryLabel = categoryTokens.length && categoryTokens.every((token) => token.includes("班")) ? "优选班" : "课程分类";
    appendCourseFact(facts, categoryLabel, categoryTokens.join("、"));
    appendCourseFact(facts, "备注", cls.remark);
    card.appendChild(facts);
    return card;
  }

  function appendCourseFact(container, label, value) {
    if (value === undefined || value === null || String(value).trim() === "") return;
    const wrapper = node("div", "course-fact");
    wrapper.append(node("dt", "", label), node("dd", "", value));
    container.appendChild(wrapper);
  }

  // category 在 API 侧已规范化为逗号连接;"交通、工程与创新世界"这类
  // 官方分类名本身含顿号,这里绝不能再按顿号拆。
  function splitCourseCategories(category) {
    return String(category || "").split(/[,，]/).map((token) => token.trim()).filter(Boolean);
  }

  async function loadTeacherCourses(id, requestID) {
    try {
      const payload = await getJSON(`/api/v1/teachers/${encodeURIComponent(id)}/courses`);
      if (requestID !== state.profileRequest) return;
      const courses = payload && Array.isArray(payload.data) ? payload.data : [];
      renderProfileCourses(courses);
    } catch (_error) {
      // 课程栏目加载失败不影响教师资料本身。
    }
  }

  function renderProfileCourses(courses) {
    if (!courses.length) return;
    const main = elements.profileContent.querySelector(".profile-main");
    if (!main) return;
    const section = node("section", "profile-section");
    section.appendChild(node("h2", "", `开课信息 (${courses.length})`));
    // 与课程列表共用同一套行组件:整行可点,含活水评分徽标、性质与分类标签。
    // 从教师主页点进去带上 ?class= 参数,课程页置顶展示该班大卡片与活水评价。
    const list = node("div", "course-list");
    for (const course of courses) list.appendChild(renderCourseCard(course, true));
    section.appendChild(list);
    main.appendChild(section);
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
      loadTeacherCourses(id, requestID);
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

  function hideAllViews() {
    elements.homeView.classList.add("hidden");
    elements.teachersView.classList.add("hidden");
    elements.profileView.classList.add("hidden");
    elements.coursesView.classList.add("hidden");
    elements.courseView.classList.add("hidden");
  }

  function setNav(section) {
    if (elements.navHome) elements.navHome.classList.toggle("active", section === "home");
    if (elements.navTeachers) elements.navTeachers.classList.toggle("active", section === "teachers");
    if (elements.navCourses) elements.navCourses.classList.toggle("active", section === "courses");
  }

  function showHome() {
    hideAllViews();
    elements.homeView.classList.remove("hidden");
    setNav("home");
    document.title = "扬华师课 · 教师与开课信息";
  }

  function showTeachers() {
    hideAllViews();
    elements.teachersView.classList.remove("hidden");
    setNav("teachers");
    document.title = "教师目录 · 扬华师课";
  }

  function showProfile() {
    hideAllViews();
    elements.profileView.classList.remove("hidden");
    setNav("teachers");
  }

  function showCourses() {
    hideAllViews();
    elements.coursesView.classList.remove("hidden");
    setNav("courses");
    document.title = "开课信息 · 扬华师课";
  }

  function showCourse() {
    hideAllViews();
    elements.courseView.classList.remove("hidden");
    setNav("courses");
  }

  function route() {
    const courseMatch = window.location.pathname.match(/^\/courses\/([^/]+)\/?$/);
    if (courseMatch) {
      showCourse();
      const pinnedClass = (new URLSearchParams(window.location.search).get("class") || "").trim();
      loadCourseDetail(decodeURIComponent(courseMatch[1]), pinnedClass);
      return;
    }
    if (/^\/courses\/?$/.test(window.location.pathname)) {
      // 支持 /courses?search=&campus=&weekday=&category= 直接带入筛选。
      const params = new URLSearchParams(window.location.search);
      state.coursePage = 1;
      state.courseSearch = (params.get("search") || "").trim();
      state.courseCategory = (params.get("category") || "").trim();
      elements.courseSearchInput.value = state.courseSearch;
      elements.courseCampusSelect.value = (params.get("campus") || "").trim();
      state.courseCampus = elements.courseCampusSelect.value;
      elements.courseWeekdaySelect.value = (params.get("weekday") || "").trim();
      state.courseWeekday = elements.courseWeekdaySelect.value;
      showCourses();
      loadMeta();
      loadCourseCategories();
      loadCourses();
      return;
    }
    // 活水评分不再单独成页,旧链接统一落到开课信息页。
    if (/^\/huoshui\/?$/.test(window.location.pathname)) {
      window.history.replaceState({}, "", "/courses");
      showCourses();
      loadMeta();
      loadCourses();
      return;
    }
    const match = window.location.pathname.match(/^\/teachers\/([^/]+)\/?$/);
    if (match) {
      showProfile();
      loadTeacherProfile(decodeURIComponent(match[1]));
      return;
    }
    if (/^\/teachers\/?$/.test(window.location.pathname)) {
      // 支持 /teachers?search=交通运输&initial=z 直接带入筛选(首页学科跑马灯在用)。
      const params = new URLSearchParams(window.location.search);
      state.page = 1;
      state.search = (params.get("search") || "").trim();
      state.initial = (params.get("initial") || "").trim().toLowerCase();
      if (!/^[a-z]$/.test(state.initial)) state.initial = "";
      elements.searchInput.value = state.search;
      elements.initialSelect.value = state.initial;
      showTeachers();
      loadMeta();
      loadTeachers();
      return;
    }
    showHome();
    loadMeta();
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

  /* ==================== 活水评分 ====================
     数据来源:活水 app.huoshui.org,由 teach-api 定时同步到数据库。
     活水不设独立页面:开课信息列表显示评分徽标,课程详情页内联展示
     评分与学生评价(无匹配不展示)。 */

  function huoshuiScoreBadge(score, className) {
    const badge = node("span", className);
    const value = Number(score);
    if (!Number.isFinite(value) || value <= 0) {
      badge.classList.add("score-none");
      badge.textContent = "-";
    } else {
      badge.classList.add(value >= 4 ? "score-good" : value >= 3 ? "score-mid" : "score-bad");
      badge.textContent = value.toFixed(2).replace(/0+$/, "").replace(/\.$/, "");
    }
    return badge;
  }

  function renderHuoshuiScoreGrid(course) {
    const scores = node("dl", "huoshui-detail-scores");
    appendHuoshuiScore(scores, "综合评分", course.rateOverall, null, true);
    appendHuoshuiScore(scores, "课程质量", course.rate1, course.reviewCount);
    appendHuoshuiScore(scores, "作业多少", course.rate2, course.reviewCount);
    appendHuoshuiScore(scores, "给分高低", course.rate3, course.reviewCount);
    appendHuoshuiScore(scores, "点名频率", course.attendanceOverall, course.attendanceCount);
    appendHuoshuiScore(scores, "作业量", course.homeworkOverall, course.homeworkCount);
    appendHuoshuiScore(scores, "考试难度", course.examOverall, course.examCount);
    appendHuoshuiScore(scores, "水课程度", course.birdOverall, course.birdCount);
    return scores;
  }

  function renderHuoshuiReviewSection(reviews) {
    const section = document.createDocumentFragment();
    section.appendChild(node("h3", "huoshui-reviews-title", "学生评价"));
    const list = node("div", "huoshui-review-list");
    const items = Array.isArray(reviews) ? reviews : [];
    if (!items.length) {
      list.appendChild(node("div", "huoshui-reviews-status", "这门课还没有具体评价内容。"));
    }
    for (const review of items) list.appendChild(renderHuoshuiReview(review));
    section.appendChild(list);
    return section;
  }

  function appendHuoshuiScore(container, label, rawValue, count, isBadge) {
    const value = Number(rawValue);
    const wrapper = node("div", "huoshui-detail-score");
    wrapper.appendChild(node("dt", "", label));
    const dd = node("dd");
    if (isBadge) {
      dd.appendChild(huoshuiScoreBadge(rawValue, "huoshui-review-score"));
    } else {
      dd.textContent = Number.isFinite(value) && value > 0 ? value.toFixed(2) : "-";
    }
    const countNumber = Number(count);
    if (Number.isFinite(countNumber) && countNumber > 0) {
      dd.appendChild(node("span", "huoshui-detail-count", ` ${formatNumber(countNumber)} 人评`));
    }
    wrapper.appendChild(dd);
    container.appendChild(wrapper);
  }

  function renderHuoshuiReview(review) {
    const card = node("article", "huoshui-review");
    const head = node("div", "huoshui-review-head");
    head.appendChild(huoshuiScoreBadge(review.ratingOverall, "huoshui-review-score"));
    const tags = node("div", "huoshui-review-tags");
    for (const [label, value] of [
      ["考试", review.examInfo],
      ["点名", review.attendance],
      ["水课", review.bird],
      ["作业", review.homework],
    ]) {
      const text = String(value || "").trim();
      if (text) tags.appendChild(node("span", "huoshui-review-tag", `${label}:${text}`));
    }
    head.appendChild(tags);
    card.appendChild(head);
    card.appendChild(node("p", "huoshui-review-text", review.comment || "(无内容)"));
    card.appendChild(node("p", "huoshui-review-foot", [
      review.authorDept, review.createdAt,
      Number(review.upVote) ? `👍 ${formatNumber(review.upVote)}` : "",
    ].filter(Boolean).join(" · ")));
    return card;
  }

  /* 开课信息联动:课程卡片与详情页展示匹配到的活水评分(无匹配不展示)。 */

  // 评分徽标可点击:传入 onClick 时表现为按钮(列表里跳到焦点班视图,
  // 课程页内滚动到对应的活水评价区)。
  function renderHuoshuiMiniBadge(huoshui, onClick) {
    const badge = huoshuiScoreBadge(huoshui.rateOverall, "huoshui-mini-badge");
    badge.appendChild(node("span", "huoshui-mini-count", `活水 · ${formatNumber(huoshui.reviewCount || 0)} 评`));
    if (onClick) {
      badge.classList.add("huoshui-mini-badge-link");
      badge.tabIndex = 0;
      badge.setAttribute("role", "button");
      badge.setAttribute("aria-label", "查看活水评分与评价");
      badge.addEventListener("click", (event) => {
        event.stopPropagation();
        onClick();
      });
      badge.addEventListener("keydown", (event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          event.stopPropagation();
          onClick();
        }
      });
    }
    return badge;
  }

  async function loadCourseHuoshuiSection(huoshui, requestID, anchor) {
    const placeholder = node("section", "profile-section course-huoshui");
    placeholder.id = `huoshui-${huoshui.objectId}`;
    placeholder.appendChild(node("h2", "", "活水评分"));
    placeholder.appendChild(node("div", "huoshui-reviews-status is-loading", "正在读取活水评价…"));
    anchor.appendChild(placeholder);
    try {
      const payload = await getJSON(`/api/v1/huoshui/courses/${encodeURIComponent(huoshui.objectId)}`);
      if (requestID !== state.courseRequest) return;
      const detail = payload && payload.data ? payload.data : payload;
      if (!detail) throw new Error("not_found");
      placeholder.replaceChildren();
      placeholder.appendChild(node("h2", "", `活水评分 (${formatNumber(detail.reviewCount || 0)} 条评价)`));
      placeholder.appendChild(renderHuoshuiScoreGrid(detail));
      placeholder.appendChild(renderHuoshuiReviewSection(detail.reviews));
    } catch (error) {
      if (requestID !== state.courseRequest) return;
      if (error && error.name === "AbortError") return;
      placeholder.replaceChildren();
      placeholder.appendChild(node("h2", "", "活水评分"));
      placeholder.appendChild(node("div", "huoshui-reviews-status", "活水评价暂时加载失败。"));
    }
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
      elements.teachersView.scrollIntoView({ block: "start" });
    }
  });
  elements.nextPage.addEventListener("click", () => {
    if (state.page < state.totalPages) {
      state.page += 1;
      loadTeachers();
      elements.teachersView.scrollIntoView({ block: "start" });
    }
  });
  elements.courseFilterForm.addEventListener("submit", (event) => {
    event.preventDefault();
    state.coursePage = 1;
    state.courseSearch = elements.courseSearchInput.value.trim();
    state.courseCampus = elements.courseCampusSelect.value;
    state.courseWeekday = elements.courseWeekdaySelect.value;
    state.courseCategory = elements.courseCategorySelect.value;
    loadCourses();
  });
  elements.courseSearchInput.addEventListener("input", () => {
    window.clearTimeout(elements.courseSearchInput.searchTimer);
    elements.courseSearchInput.searchTimer = window.setTimeout(() => {
      state.coursePage = 1;
      state.courseSearch = elements.courseSearchInput.value.trim();
      loadCourses();
    }, 280);
  });
  elements.courseCampusSelect.addEventListener("change", () => {
    state.coursePage = 1;
    state.courseCampus = elements.courseCampusSelect.value;
    loadCourses();
  });
  elements.courseWeekdaySelect.addEventListener("change", () => {
    state.coursePage = 1;
    state.courseWeekday = elements.courseWeekdaySelect.value;
    loadCourses();
  });
  elements.courseCategorySelect.addEventListener("change", () => {
    state.coursePage = 1;
    state.courseCategory = elements.courseCategorySelect.value;
    loadCourses();
  });
  elements.courseResetButton.addEventListener("click", resetCourseFilters);
  elements.courseEmptyReset.addEventListener("click", resetCourseFilters);
  elements.courseRetryButton.addEventListener("click", () => loadCourses());
  if ("IntersectionObserver" in window) {
    const observer = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        if (entry.isIntersecting) loadCourses(true);
      }
    }, { rootMargin: "600px 0px" });
    observer.observe(elements.courseSentinel);
  } else {
    // 不支持 IntersectionObserver 的老浏览器退化为点击加载。
    elements.courseSentinel.addEventListener("click", () => loadCourses(true));
  }
  window.addEventListener("popstate", route);
  route();
})();
