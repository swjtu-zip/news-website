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
    courseTotalPages: 0,
    courseTotal: 0,
    courseListRequest: 0,
    courseListController: null,
    courseRequest: 0,
    huoshuiPage: 1,
    huoshuiPageSize: 50,
    huoshuiSearch: "",
    huoshuiDept: "",
    huoshuiSort: "reviews",
    huoshuiTotalPages: 0,
    huoshuiTotal: 0,
    huoshuiLoaded: false,
    huoshuiLoading: false,
    huoshuiCourses: [],
    huoshuiStats: null,
    huoshuiMeta: null,
    huoshuiReviews: null,
    huoshuiReviewsLoading: null,
    huoshuiFiltered: [],
  };

  const elements = {
    directoryView: document.getElementById("directory-view"),
    profileView: document.getElementById("profile-view"),
    coursesView: document.getElementById("courses-view"),
    courseView: document.getElementById("course-view"),
    navTeachers: document.getElementById("nav-teachers"),
    navCourses: document.getElementById("nav-courses"),
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
    asideCourseBadge: document.getElementById("aside-course-badge"),
    asideCourseCount: document.getElementById("aside-course-count"),
    coursesTerm: document.getElementById("courses-term"),
    courseFilterForm: document.getElementById("course-filter-form"),
    courseSearchInput: document.getElementById("course-search-input"),
    courseCampusSelect: document.getElementById("course-campus-select"),
    courseWeekdaySelect: document.getElementById("course-weekday-select"),
    courseResetButton: document.getElementById("course-reset-button"),
    courseEmptyReset: document.getElementById("course-empty-reset"),
    courseRetryButton: document.getElementById("course-retry-button"),
    courseListStatus: document.getElementById("course-list-status"),
    courseList: document.getElementById("course-list"),
    courseEmptyState: document.getElementById("course-empty-state"),
    courseErrorState: document.getElementById("course-error-state"),
    courseErrorMessage: document.getElementById("course-error-message"),
    coursePagination: document.getElementById("course-pagination"),
    coursePreviousPage: document.getElementById("course-previous-page"),
    courseNextPage: document.getElementById("course-next-page"),
    coursePageLabel: document.getElementById("course-page-label"),
    courseLoading: document.getElementById("course-loading"),
    courseError: document.getElementById("course-error"),
    courseErrorMessageDetail: document.getElementById("course-error-message-detail"),
    coursePage: document.getElementById("course-page"),
    courseContent: document.getElementById("course-content"),
    courseBreadcrumbName: document.getElementById("course-breadcrumb-name"),
    huoshuiView: document.getElementById("huoshui-view"),
    navHuoshui: document.getElementById("nav-huoshui"),
    huoshuiUpdated: document.getElementById("huoshui-updated"),
    huoshuiFilterForm: document.getElementById("huoshui-filter-form"),
    huoshuiSearchInput: document.getElementById("huoshui-search-input"),
    huoshuiDeptSelect: document.getElementById("huoshui-dept-select"),
    huoshuiSortSelect: document.getElementById("huoshui-sort-select"),
    huoshuiResetButton: document.getElementById("huoshui-reset-button"),
    huoshuiEmptyReset: document.getElementById("huoshui-empty-reset"),
    huoshuiRetryButton: document.getElementById("huoshui-retry-button"),
    huoshuiListStatus: document.getElementById("huoshui-list-status"),
    huoshuiList: document.getElementById("huoshui-list"),
    huoshuiEmptyState: document.getElementById("huoshui-empty-state"),
    huoshuiErrorState: document.getElementById("huoshui-error-state"),
    huoshuiErrorMessage: document.getElementById("huoshui-error-message"),
    huoshuiPagination: document.getElementById("huoshui-pagination"),
    huoshuiPreviousPage: document.getElementById("huoshui-previous-page"),
    huoshuiNextPage: document.getElementById("huoshui-next-page"),
    huoshuiPageLabel: document.getElementById("huoshui-page-label"),
    huoshuiModal: document.getElementById("huoshui-modal"),
    huoshuiModalOverlay: document.getElementById("huoshui-modal-overlay"),
    huoshuiModalClose: document.getElementById("huoshui-modal-close"),
    huoshuiModalContent: document.getElementById("huoshui-modal-content"),
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
        elements.datasetUpdated.textContent = generated ? `更新时间 ${generated}` : "更新时间读取中";
      }
      if (Number.isFinite(Number(meta.course_count)) && Number(meta.course_count) > 0) {
        if (elements.asideCourseCount) elements.asideCourseCount.textContent = formatNumber(meta.course_count);
        if (elements.coursesTerm && meta.courses_term) elements.coursesTerm.textContent = `${meta.courses_term} · 选修课`;
        if (elements.asideCourseBadge && meta.courses_term) elements.asideCourseBadge.textContent = meta.courses_term;
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

  async function loadCourses() {
    const requestID = ++state.courseListRequest;
    if (state.courseListController) state.courseListController.abort();
    state.courseListController = new AbortController();
    const params = new URLSearchParams({ page: String(state.coursePage), page_size: String(state.coursePageSize) });
    if (state.courseSearch) params.set("search", state.courseSearch);
    if (state.courseCampus) params.set("campus", state.courseCampus);
    if (state.courseWeekday) params.set("weekday", state.courseWeekday);

    setCourseLoading(true);
    try {
      const payload = await getJSON(`/api/v1/courses?${params.toString()}`, state.courseListController.signal);
      if (requestID !== state.courseListRequest) return;
      const page = payload || { data: [], pagination: {} };
      const pagination = page.pagination || {};
      state.courseTotal = Number(pagination.total) || 0;
      state.courseTotalPages = Number(pagination.total_pages) || 0;
      renderCourses(Array.isArray(page.data) ? page.data : []);
      renderCoursePagination();
      if (state.courseTotal > 0) {
        elements.courseListStatus.textContent = `共 ${formatNumber(state.courseTotal)} 门课 · 第 ${state.coursePage} / ${state.courseTotalPages} 页`;
      } else {
        elements.courseListStatus.textContent = "当前筛选条件下没有结果";
      }
    } catch (error) {
      if (error && error.name === "AbortError") return;
      if (requestID !== state.courseListRequest) return;
      elements.courseList.replaceChildren();
      elements.coursePagination.classList.add("hidden");
      elements.courseEmptyState.classList.add("hidden");
      elements.courseErrorState.classList.remove("hidden");
      elements.courseErrorMessage.textContent = friendlyError(error);
      elements.courseListStatus.textContent = "加载失败";
    } finally {
      if (requestID === state.courseListRequest) setCourseLoading(false);
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

  function renderCourses(courses) {
    elements.courseErrorState.classList.add("hidden");
    elements.courseEmptyState.classList.toggle("hidden", courses.length !== 0);
    elements.courseList.replaceChildren();
    for (const course of courses) elements.courseList.appendChild(renderCourseCard(course));
  }

  function renderCourseCard(course) {
    const article = node("article", "course-row");
    const main = node("div", "course-row-main");
    const title = node("h3");
    const link = node("a", "course-name-link", course.course_name || "未命名课程");
    link.href = `/courses/${encodeURIComponent(course.id)}`;
    title.appendChild(link);
    main.appendChild(title);
    const meta = [course.course_code, course.college, course.credit ? `${course.credit} 学分` : ""].filter(Boolean).join(" · ");
    main.appendChild(node("p", "course-row-meta", meta));
    article.appendChild(main);

    const schedule = node("div", "course-row-schedule");
    for (const value of [course.campus, course.weekday, course.periods ? `${course.periods} 节` : ""].filter(Boolean)) {
      schedule.appendChild(node("span", "course-chip", value));
    }
    article.appendChild(schedule);

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
    return article;
  }

  function renderCoursePagination() {
    const hasPages = state.courseTotalPages > 1;
    elements.coursePagination.classList.toggle("hidden", !hasPages);
    elements.coursePreviousPage.disabled = state.coursePage <= 1;
    elements.courseNextPage.disabled = state.coursePage >= state.courseTotalPages;
    elements.coursePageLabel.textContent = `第 ${state.coursePage} / ${state.courseTotalPages || 1} 页`;
  }

  function resetCourseFilters() {
    state.coursePage = 1;
    state.courseSearch = "";
    state.courseCampus = "";
    state.courseWeekday = "";
    elements.courseSearchInput.value = "";
    elements.courseCampusSelect.value = "";
    elements.courseWeekdaySelect.value = "";
    loadCourses();
  }

  async function loadCourseDetail(id) {
    const requestID = ++state.courseRequest;
    elements.courseLoading.classList.remove("hidden");
    elements.coursePage.classList.add("hidden");
    elements.courseError.classList.add("hidden");
    elements.courseContent.replaceChildren();
    try {
      const payload = await getJSON(`/api/v1/courses/${encodeURIComponent(id)}`);
      if (requestID !== state.courseRequest) return;
      const course = payload && payload.data ? payload.data : payload;
      if (!course) throw new Error("not_found");
      renderCourseDetail(course);
      elements.courseLoading.classList.add("hidden");
      elements.coursePage.classList.remove("hidden");
      elements.courseBreadcrumbName.textContent = course.course_name || "课程详情";
      document.title = `${course.course_name || "课程详情"} · 扬华师课`;
    } catch (error) {
      if (requestID !== state.courseRequest) return;
      elements.courseLoading.classList.add("hidden");
      elements.courseError.classList.remove("hidden");
      elements.courseErrorMessageDetail.textContent = friendlyError(error);
    }
  }

  function renderCourseDetail(course) {
    const content = document.createDocumentFragment();
    const hero = node("section", "course-hero");
    const headline = node("div", "course-headline");
    headline.appendChild(node("p", "eyebrow", "课程详情"));
    const title = node("h1", "", course.course_name || "未命名课程");
    title.id = "course-page-title";
    headline.appendChild(title);
    headline.appendChild(node("p", "course-hero-meta", [
      course.course_code,
      course.id ? `教学班号 ${course.id}` : "",
      course.credit ? `${course.credit} 学分` : "",
      course.term,
    ].filter(Boolean).join(" · ")));
    const categories = String(course.category || "").split(/[,，]/).map((item) => item.trim()).filter(Boolean);
    if (categories.length) {
      const tags = node("div", "course-hero-tags");
      for (const category of categories) tags.appendChild(node("span", "course-hero-tag", category));
      headline.appendChild(tags);
    }
    hero.appendChild(headline);

    const teacherCard = node("div", "course-teacher-card");
    teacherCard.appendChild(node("span", "course-teacher-label", "授课教师"));
    teacherCard.appendChild(node("strong", "", course.teacher_name || "未命名"));
    if (course.teacher_title) teacherCard.appendChild(node("span", "course-teacher-title", course.teacher_title));
    if (course.teacher_id) {
      const teacherLink = node("a", "course-teacher-link", "查看教师主页 →");
      teacherLink.href = `/teachers/${encodeURIComponent(course.teacher_id)}`;
      teacherCard.appendChild(teacherLink);
    } else {
      teacherCard.appendChild(node("span", "course-teacher-note", "暂未收录教师主页"));
    }
    hero.appendChild(teacherCard);
    content.appendChild(hero);

    const facts = node("dl", "course-facts");
    appendCourseFact(facts, "学期", course.term);
    appendCourseFact(facts, "开课学院", course.college);
    appendCourseFact(facts, "课程代码", course.course_code);
    appendCourseFact(facts, "教学班号", course.id);
    appendCourseFact(facts, "教学班", course.class_num);
    appendCourseFact(facts, "学分", course.credit ? `${course.credit}` : "");
    appendCourseFact(facts, "总学时", course.hours_total ? `${course.hours_total}` : "");
    appendCourseFact(facts, "周学时", course.hours_week ? `${course.hours_week}` : "");
    appendCourseFact(facts, "周次", course.weeks);
    appendCourseFact(facts, "星期", course.weekday);
    appendCourseFact(facts, "节次", course.periods);
    appendCourseFact(facts, "校区", course.campus);
    appendCourseFact(facts, "上课时间", course.schedule_text);
    appendCourseFact(facts, "考核方式", course.assessment);
    appendCourseFact(facts, "课程性质", course.nature);
    appendCourseFact(facts, "课程分类", course.category);
    appendCourseFact(facts, "备注", course.remark);
    const factsCard = node("section", "course-facts-card");
    factsCard.appendChild(facts);
    content.appendChild(factsCard);
    elements.courseContent.replaceChildren(content);
  }

  function appendCourseFact(container, label, value) {
    if (value === undefined || value === null || String(value).trim() === "") return;
    const wrapper = node("div", "course-fact");
    wrapper.append(node("dt", "", label), node("dd", "", value));
    container.appendChild(wrapper);
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
    const list = node("div", "profile-course-list");
    for (const course of courses) {
      const link = node("a", "profile-course-item");
      link.href = `/courses/${encodeURIComponent(course.id)}`;
      const name = node("span", "profile-course-name", course.course_name || "未命名课程");
      link.appendChild(name);
      link.appendChild(node("span", "profile-course-meta", [
        course.term, course.campus, course.weekday, course.periods ? `${course.periods} 节` : "",
      ].filter(Boolean).join(" · ")));
      list.appendChild(link);
    }
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
    elements.directoryView.classList.add("hidden");
    elements.profileView.classList.add("hidden");
    elements.coursesView.classList.add("hidden");
    elements.courseView.classList.add("hidden");
    elements.huoshuiView.classList.add("hidden");
  }

  function setNav(section) {
    if (!elements.navTeachers || !elements.navCourses) return;
    elements.navTeachers.classList.toggle("active", section === "teachers");
    elements.navCourses.classList.toggle("active", section === "courses");
    if (elements.navHuoshui) elements.navHuoshui.classList.toggle("active", section === "huoshui");
  }

  function showDirectory() {
    hideAllViews();
    elements.directoryView.classList.remove("hidden");
    setNav("teachers");
    document.title = "扬华师课 · 教师与开课信息";
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
      loadCourseDetail(decodeURIComponent(courseMatch[1]));
      return;
    }
    if (/^\/courses\/?$/.test(window.location.pathname)) {
      showCourses();
      loadMeta();
      loadCourses();
      return;
    }
    if (/^\/huoshui\/?$/.test(window.location.pathname)) {
      showHuoshui();
      loadHuoshui();
      return;
    }
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

  /* ==================== 活水评分 ====================
     数据来源:活水 app.huoshui.org,由 huoshui-scraper 每日抓取,
     本站通过 scripts/update-huoshui.sh 同步到 /data/huoshui/。
     字段为中文键,评价通过 (课程名称, 授课教师) 匹配课程。 */

  const HUOSHUI_DATA_BASE = "/data/huoshui";

  async function fetchHuoshuiJSON(name) {
    const response = await fetch(`${HUOSHUI_DATA_BASE}/${name}`, {
      method: "GET",
      headers: { Accept: "application/json" },
      cache: "default",
    });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    return response.json();
  }

  function showHuoshui() {
    hideAllViews();
    elements.huoshuiView.classList.remove("hidden");
    setNav("huoshui");
    document.title = "活水评分 · 扬华师课";
  }

  async function loadHuoshui() {
    if (state.huoshuiLoaded || state.huoshuiLoading) return;
    state.huoshuiLoading = true;
    setHuoshuiLoading(true);
    try {
      const [meta, stats, courses] = await Promise.all([
        fetchHuoshuiJSON("meta.json").catch(() => null),
        fetchHuoshuiJSON("stats.json").catch(() => null),
        fetchHuoshuiJSON("courses.json"),
      ]);
      state.huoshuiMeta = meta;
      state.huoshuiStats = stats;
      state.huoshuiCourses = Array.isArray(courses) ? courses : [];
      state.huoshuiLoaded = true;
      renderHuoshuiMeta();
      populateHuoshuiDepts();
      applyHuoshuiFilters();
    } catch (error) {
      elements.huoshuiList.replaceChildren();
      elements.huoshuiPagination.classList.add("hidden");
      elements.huoshuiEmptyState.classList.add("hidden");
      elements.huoshuiErrorState.classList.remove("hidden");
      elements.huoshuiErrorMessage.textContent = friendlyError(error);
      elements.huoshuiListStatus.textContent = "加载失败";
    } finally {
      state.huoshuiLoading = false;
      elements.huoshuiListStatus.classList.remove("is-loading");
    }
  }

  function setHuoshuiLoading(loading) {
    elements.huoshuiListStatus.classList.toggle("is-loading", loading);
    if (loading) {
      elements.huoshuiListStatus.textContent = "正在读取活水评分数据…";
      elements.huoshuiEmptyState.classList.add("hidden");
      elements.huoshuiErrorState.classList.add("hidden");
    }
  }

  function renderHuoshuiMeta() {
    const parts = [];
    if (state.huoshuiStats && Array.isArray(state.huoshuiStats.院系统计)) {
      const totals = state.huoshuiStats.院系统计.reduce(
        (acc, row) => ({ courses: acc.courses + (Number(row.课程数) || 0), reviews: acc.reviews + (Number(row.评价数) || 0) }),
        { courses: 0, reviews: 0 },
      );
      if (totals.reviews > 0) parts.push(`共 ${formatNumber(totals.courses)} 门课程 · ${formatNumber(totals.reviews)} 条评价`);
    }
    const upstream = state.huoshuiMeta ? formatDate(state.huoshuiMeta.upstreamCommittedAt) : "";
    const fetched = state.huoshuiMeta ? formatDate(state.huoshuiMeta.fetchedAt) : "";
    if (upstream) parts.push(`数据更新于 ${upstream}`);
    else if (fetched) parts.push(`同步于 ${fetched}`);
    elements.huoshuiUpdated.textContent = parts.length ? parts.join(" · ") : "数据更新时间未知";
  }

  function populateHuoshuiDepts() {
    const counts = new Map();
    for (const course of state.huoshuiCourses) {
      const dept = String(course.所属院系 || "").trim();
      if (dept) counts.set(dept, (counts.get(dept) || 0) + 1);
    }
    const depts = [...counts.entries()].sort((a, b) => b[1] - a[1]);
    elements.huoshuiDeptSelect.replaceChildren(node("option", "", "全部院系"));
    elements.huoshuiDeptSelect.firstChild.value = "";
    for (const [dept] of depts) {
      const option = node("option", "", dept);
      option.value = dept;
      elements.huoshuiDeptSelect.appendChild(option);
    }
  }

  function huoshuiScoreNumber(course, key) {
    const value = Number(course[key]);
    return Number.isFinite(value) ? value : 0;
  }

  function applyHuoshuiFilters() {
    const keyword = state.huoshuiSearch.toLowerCase();
    let list = state.huoshuiCourses.filter((course) => {
      if (state.huoshuiDept && String(course.所属院系 || "") !== state.huoshuiDept) return false;
      if (keyword) {
        const haystack = `${course.课程名称 || ""} ${course.授课教师 || ""}`.toLowerCase();
        if (!haystack.includes(keyword)) return false;
      }
      return true;
    });
    if (state.huoshuiSort === "score" || state.huoshuiSort === "grading") {
      // 评分排序只保留评价数 >= 3 的课程,避免单条满分刷榜(与活水一致)。
      const key = state.huoshuiSort === "score" ? "综合评分" : "给分高低";
      list = list.filter((course) => Number(course.评价数量) >= 3);
      list.sort((a, b) => huoshuiScoreNumber(b, key) - huoshuiScoreNumber(a, key));
    } else {
      list.sort((a, b) => (Number(b.评价数量) || 0) - (Number(a.评价数量) || 0));
    }
    state.huoshuiFiltered = list;
    state.huoshuiTotal = list.length;
    state.huoshuiTotalPages = Math.max(1, Math.ceil(list.length / state.huoshuiPageSize));
    if (state.huoshuiPage > state.huoshuiTotalPages) state.huoshuiPage = state.huoshuiTotalPages;
    renderHuoshuiPage();
  }

  function renderHuoshuiPage() {
    const start = (state.huoshuiPage - 1) * state.huoshuiPageSize;
    const pageItems = state.huoshuiFiltered.slice(start, start + state.huoshuiPageSize);
    elements.huoshuiErrorState.classList.add("hidden");
    elements.huoshuiEmptyState.classList.toggle("hidden", pageItems.length !== 0);
    elements.huoshuiList.replaceChildren();
    for (const course of pageItems) elements.huoshuiList.appendChild(renderHuoshuiCard(course));
    if (state.huoshuiTotal > 0) {
      elements.huoshuiListStatus.textContent = `共 ${formatNumber(state.huoshuiTotal)} 门课 · 第 ${state.huoshuiPage} / ${state.huoshuiTotalPages} 页`;
    } else {
      elements.huoshuiListStatus.textContent = "当前筛选条件下没有结果";
    }
    renderHuoshuiPagination();
  }

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

  function renderHuoshuiCard(course) {
    const article = node("article", "huoshui-row");
    article.tabIndex = 0;
    article.setAttribute("role", "button");

    const main = node("div", "huoshui-row-main");
    const title = node("h3");
    title.appendChild(node("span", "huoshui-course-name", course.课程名称 || "未命名课程"));
    main.appendChild(title);
    main.appendChild(node("p", "huoshui-row-meta", [
      course.授课教师, course.所属院系, course.教师职称,
      `${formatNumber(course.评价数量 || 0)} 条评价`,
    ].filter(Boolean).join(" · ")));
    article.appendChild(main);

    const subs = node("div", "huoshui-subscores");
    for (const [label, key] of [["课程质量", "课程质量"], ["作业多少", "作业多少"], ["给分高低", "给分高低"]]) {
      subs.appendChild(renderHuoshuiSubscore(label, course[key]));
    }
    article.appendChild(subs);

    article.appendChild(huoshuiScoreBadge(course.综合评分, "score-badge"));

    const open = () => openHuoshuiDetail(course);
    article.addEventListener("click", open);
    article.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        open();
      }
    });
    return article;
  }

  function renderHuoshuiSubscore(label, rawValue) {
    const wrapper = node("div", "huoshui-subscore");
    wrapper.appendChild(node("span", "huoshui-subscore-label", label));
    const bar = node("span", "huoshui-subscore-bar");
    const fill = document.createElement("i");
    const value = Number(rawValue);
    const percent = Number.isFinite(value) && value > 0 ? Math.min(100, (value / 5) * 100) : 0;
    fill.style.width = `${percent}%`;
    if (value >= 4) fill.style.background = "#3ecf7a";
    else if (value >= 3) fill.style.background = "var(--sun)";
    else if (value > 0) fill.style.background = "var(--coral)";
    bar.appendChild(fill);
    wrapper.appendChild(bar);
    wrapper.appendChild(node("span", "huoshui-subscore-value", Number.isFinite(value) && value > 0 ? value.toFixed(1) : "-"));
    return wrapper;
  }

  function renderHuoshuiPagination() {
    const hasPages = state.huoshuiTotalPages > 1;
    elements.huoshuiPagination.classList.toggle("hidden", !hasPages);
    elements.huoshuiPreviousPage.disabled = state.huoshuiPage <= 1;
    elements.huoshuiNextPage.disabled = state.huoshuiPage >= state.huoshuiTotalPages;
    elements.huoshuiPageLabel.textContent = `第 ${state.huoshuiPage} / ${state.huoshuiTotalPages || 1} 页`;
  }

  function resetHuoshuiFilters() {
    state.huoshuiPage = 1;
    state.huoshuiSearch = "";
    state.huoshuiDept = "";
    state.huoshuiSort = "reviews";
    elements.huoshuiSearchInput.value = "";
    elements.huoshuiDeptSelect.value = "";
    elements.huoshuiSortSelect.value = "reviews";
    if (state.huoshuiLoaded) applyHuoshuiFilters();
  }

  async function ensureHuoshuiReviews() {
    if (state.huoshuiReviews) return state.huoshuiReviews;
    if (!state.huoshuiReviewsLoading) {
      state.huoshuiReviewsLoading = fetchHuoshuiJSON("reviews.json").then((payload) => {
        state.huoshuiReviews = Array.isArray(payload) ? payload : [];
        return state.huoshuiReviews;
      }).catch((error) => {
        state.huoshuiReviewsLoading = null;
        throw error;
      });
    }
    return state.huoshuiReviewsLoading;
  }

  function huoshuiCourseKey(name, teacher) {
    return `${String(name || "").trim()}::${String(teacher || "").trim()}`;
  }

  function openHuoshuiDetail(course) {
    renderHuoshuiDetail(course);
    elements.huoshuiModal.classList.remove("hidden");
    document.body.style.overflow = "hidden";
    elements.huoshuiModalClose.focus();
  }

  function closeHuoshuiDetail() {
    elements.huoshuiModal.classList.add("hidden");
    document.body.style.overflow = "";
  }

  function renderHuoshuiDetail(course) {
    const content = document.createDocumentFragment();

    const head = node("div", "huoshui-detail-head");
    const title = node("h2", "", course.课程名称 || "未命名课程");
    title.id = "huoshui-modal-title";
    head.appendChild(title);
    head.appendChild(node("p", "huoshui-detail-meta", [
      course.授课教师, course.所属院系, course.教师职称,
      `${formatNumber(course.评价数量 || 0)} 条评价`,
    ].filter(Boolean).join(" · ")));
    content.appendChild(head);

    const scores = node("dl", "huoshui-detail-scores");
    appendHuoshuiScore(scores, "综合评分", course.综合评分, null, true);
    appendHuoshuiScore(scores, "课程质量", course.课程质量, course.评价数量);
    appendHuoshuiScore(scores, "作业多少", course.作业多少, course.评价数量);
    appendHuoshuiScore(scores, "给分高低", course.给分高低, course.评价数量);
    appendHuoshuiScore(scores, "点名频率", course.点名频率, course.点名评价数);
    appendHuoshuiScore(scores, "作业量", course.作业量, course.作业评价数);
    appendHuoshuiScore(scores, "考试难度", course.考试难度, course.考试评价数);
    appendHuoshuiScore(scores, "水课程度", course.水课程度, course.水课评价数);
    content.appendChild(scores);

    content.appendChild(node("h3", "huoshui-reviews-title", "学生评价"));
    const reviewsStatus = node("div", "huoshui-reviews-status is-loading", "正在读取评价数据(约 12MB,首次打开需要一点时间)…");
    content.appendChild(reviewsStatus);
    elements.huoshuiModalContent.replaceChildren(content);

    ensureHuoshuiReviews()
      .then((reviews) => {
        if (elements.huoshuiModal.classList.contains("hidden")) return;
        const key = huoshuiCourseKey(course.课程名称, course.授课教师);
        const matched = reviews
          .filter((review) => huoshuiCourseKey(review.课程名称, review.授课教师) === key)
          .sort((a, b) => (Number(b.点赞数) || 0) - (Number(a.点赞数) || 0));
        const list = node("div", "huoshui-review-list");
        if (!matched.length) {
          list.appendChild(node("div", "huoshui-reviews-status", "这门课还没有具体评价内容。"));
        }
        for (const review of matched) list.appendChild(renderHuoshuiReview(review));
        reviewsStatus.replaceWith(list);
      })
      .catch(() => {
        if (elements.huoshuiModal.classList.contains("hidden")) return;
        reviewsStatus.classList.remove("is-loading");
        reviewsStatus.textContent = "评价数据加载失败,请关闭后重试。";
      });
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
    head.appendChild(huoshuiScoreBadge(review.综合评分, "huoshui-review-score"));
    const tags = node("div", "huoshui-review-tags");
    for (const [label, value] of [
      ["考试", review.考试信息],
      ["点名", review.点名情况],
      ["水课", review.水课程度],
      ["作业", review.作业情况],
    ]) {
      const text = String(value || "").trim();
      if (text) tags.appendChild(node("span", "huoshui-review-tag", `${label}:${text}`));
    }
    head.appendChild(tags);
    card.appendChild(head);
    card.appendChild(node("p", "huoshui-review-text", review.评价内容 || "(无内容)"));
    card.appendChild(node("p", "huoshui-review-foot", [
      review.评价者院系, review.评价时间,
      Number(review.点赞数) ? `👍 ${formatNumber(review.点赞数)}` : "",
    ].filter(Boolean).join(" · ")));
    return card;
  }

  elements.huoshuiFilterForm.addEventListener("submit", (event) => {
    event.preventDefault();
    state.huoshuiPage = 1;
    state.huoshuiSearch = elements.huoshuiSearchInput.value.trim();
    state.huoshuiDept = elements.huoshuiDeptSelect.value;
    state.huoshuiSort = elements.huoshuiSortSelect.value;
    if (state.huoshuiLoaded) applyHuoshuiFilters();
  });
  elements.huoshuiSearchInput.addEventListener("input", () => {
    window.clearTimeout(elements.huoshuiSearchInput.searchTimer);
    elements.huoshuiSearchInput.searchTimer = window.setTimeout(() => {
      state.huoshuiPage = 1;
      state.huoshuiSearch = elements.huoshuiSearchInput.value.trim();
      if (state.huoshuiLoaded) applyHuoshuiFilters();
    }, 280);
  });
  elements.huoshuiDeptSelect.addEventListener("change", () => {
    state.huoshuiPage = 1;
    state.huoshuiDept = elements.huoshuiDeptSelect.value;
    if (state.huoshuiLoaded) applyHuoshuiFilters();
  });
  elements.huoshuiSortSelect.addEventListener("change", () => {
    state.huoshuiPage = 1;
    state.huoshuiSort = elements.huoshuiSortSelect.value;
    if (state.huoshuiLoaded) applyHuoshuiFilters();
  });
  elements.huoshuiResetButton.addEventListener("click", resetHuoshuiFilters);
  elements.huoshuiEmptyReset.addEventListener("click", resetHuoshuiFilters);
  elements.huoshuiRetryButton.addEventListener("click", loadHuoshui);
  elements.huoshuiPreviousPage.addEventListener("click", () => {
    if (state.huoshuiPage > 1) {
      state.huoshuiPage -= 1;
      renderHuoshuiPage();
      elements.huoshuiView.scrollIntoView({ block: "start" });
    }
  });
  elements.huoshuiNextPage.addEventListener("click", () => {
    if (state.huoshuiPage < state.huoshuiTotalPages) {
      state.huoshuiPage += 1;
      renderHuoshuiPage();
      elements.huoshuiView.scrollIntoView({ block: "start" });
    }
  });
  elements.huoshuiModalClose.addEventListener("click", closeHuoshuiDetail);
  elements.huoshuiModalOverlay.addEventListener("click", closeHuoshuiDetail);
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && !elements.huoshuiModal.classList.contains("hidden")) closeHuoshuiDetail();
  });

  /* ================== 活水评分 END ================== */

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
  elements.courseFilterForm.addEventListener("submit", (event) => {
    event.preventDefault();
    state.coursePage = 1;
    state.courseSearch = elements.courseSearchInput.value.trim();
    state.courseCampus = elements.courseCampusSelect.value;
    state.courseWeekday = elements.courseWeekdaySelect.value;
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
  elements.courseResetButton.addEventListener("click", resetCourseFilters);
  elements.courseEmptyReset.addEventListener("click", resetCourseFilters);
  elements.courseRetryButton.addEventListener("click", loadCourses);
  elements.coursePreviousPage.addEventListener("click", () => {
    if (state.coursePage > 1) {
      state.coursePage -= 1;
      loadCourses();
      elements.coursesView.scrollIntoView({ block: "start" });
    }
  });
  elements.courseNextPage.addEventListener("click", () => {
    if (state.coursePage < state.courseTotalPages) {
      state.coursePage += 1;
      loadCourses();
      elements.coursesView.scrollIntoView({ block: "start" });
    }
  });
  window.addEventListener("popstate", route);
  route();
})();
