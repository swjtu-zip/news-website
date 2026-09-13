// MCP (Model Context Protocol) endpoint: exposes the same read-only teaching
// directory as the REST API to AI assistants. Mounted at /mcp on the same
// HTTP server; the transport is streamable HTTP in stateless JSON mode so it
// works behind the teach-web nginx proxy without session affinity or SSE.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"teach-api/internal/store"
)

type searchTeachersInput struct {
	Search   string `json:"search,omitempty" jsonschema:"搜索关键词,匹配姓名、学院、职称"`
	College  string `json:"college,omitempty" jsonschema:"按学院过滤,如 计算机学院"`
	Initial  string `json:"initial,omitempty" jsonschema:"姓氏拼音首字母 a-z"`
	Page     int    `json:"page,omitempty" jsonschema:"页码,从 1 开始"`
	PageSize int    `json:"page_size,omitempty" jsonschema:"每页条数,默认 24,最大 100"`
}

type teacherIDInput struct {
	ID string `json:"id" jsonschema:"教师 ID,来自 search_teachers 结果"`
}

type searchCoursesInput struct {
	Search   string `json:"search,omitempty" jsonschema:"搜索关键词,匹配课程名、课程代码、教师名、开课学院"`
	Campus   string `json:"campus,omitempty" jsonschema:"校区,如 犀浦、九里、东部"`
	Weekday  string `json:"weekday,omitempty" jsonschema:"星期,如 星期一"`
	College  string `json:"college,omitempty" jsonschema:"开课学院"`
	Category string `json:"category,omitempty" jsonschema:"课程分类,如 通识课、体育课程;可选值见 list_course_categories"`
	Page     int    `json:"page,omitempty" jsonschema:"页码,从 1 开始"`
	PageSize int    `json:"page_size,omitempty" jsonschema:"每页条数,默认 24,最大 100"`
}

type courseIDInput struct {
	ID string `json:"id" jsonschema:"教学班号,来自 search_courses 结果"`
}

type courseCodeInput struct {
	CourseCode string `json:"course_code" jsonschema:"课程代码(全局稳定),来自 search_courses 结果的 course_code 字段"`
}

type huoshuiIDInput struct {
	ObjectID string `json:"object_id" jsonschema:"活水课程 ID,来自课程详情的 huoshui.objectId 字段"`
}

type teacherCoursesOutput struct {
	Courses []store.CourseSummary `json:"courses"`
}

type courseCategoriesOutput struct {
	Categories []string `json:"categories"`
}

func newMCPHandler(s *store.Store) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "swjtu-teach",
		Version: "1.0.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_teachers",
		Description: "搜索西南交通大学教师目录,返回分页的教师列表(姓名、职称、学院、研究方向等)。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input searchTeachersInput) (*mcp.CallToolResult, store.TeacherPage, error) {
		filter, err := teacherFilterFromMCP(input)
		if err != nil {
			return nil, store.TeacherPage{}, err
		}
		page, err := s.ListTeachers(ctx, filter)
		if err != nil {
			return nil, store.TeacherPage{}, fmt.Errorf("internal_error")
		}
		return nil, page, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_teacher",
		Description: "获取某位教师的完整主页资料:简介、研究方向、公开联系方式、办公地点等。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input teacherIDInput) (*mcp.CallToolResult, store.Teacher, error) {
		teacher, err := s.GetTeacher(ctx, input.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.Teacher{}, fmt.Errorf("not_found")
		}
		if err != nil {
			return nil, store.Teacher{}, fmt.Errorf("internal_error")
		}
		return nil, teacher, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_teacher_courses",
		Description: "列出某位教师当前学期的授课班级(课程名、上课时间、校区、学分)。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input teacherIDInput) (*mcp.CallToolResult, teacherCoursesOutput, error) {
		courses, err := s.ListTeacherCourses(ctx, input.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, teacherCoursesOutput{}, fmt.Errorf("not_found")
		}
		if err != nil {
			return nil, teacherCoursesOutput{}, fmt.Errorf("internal_error")
		}
		return nil, teacherCoursesOutput{Courses: courses}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_courses",
		Description: "搜索当前学期的开课信息,按课程聚合一行一门课(含班数与全部任课教师),可按课程名、教师、校区、星期筛选。拿到 course_code 后用 get_course_classes 查看各班详情与活水评分。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input searchCoursesInput) (*mcp.CallToolResult, store.CourseGroupPage, error) {
		filter, err := courseFilterFromMCP(input)
		if err != nil {
			return nil, store.CourseGroupPage{}, err
		}
		page, err := s.ListCourses(ctx, filter)
		if err != nil {
			return nil, store.CourseGroupPage{}, fmt.Errorf("internal_error")
		}
		return nil, page, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_course_categories",
		Description: "列出开课信息里所有可选的课程分类(通识课、体育课程等),配合 search_courses 的 category 参数使用。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, courseCategoriesOutput, error) {
		categories, err := s.ListCourseCategories(ctx)
		if err != nil {
			return nil, courseCategoriesOutput{}, fmt.Errorf("internal_error")
		}
		return nil, courseCategoriesOutput{Categories: categories}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_course",
		Description: "获取单个教学班的完整信息:上课时间地点、考核方式、学分学时,以及匹配到的活水评分摘要(huoshui 字段)。教学班号每学期会重新分配,跨学期引用课程请改用 get_course_classes(课程代码全局稳定)。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input courseIDInput) (*mcp.CallToolResult, store.Course, error) {
		course, err := s.GetCourse(ctx, input.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.Course{}, fmt.Errorf("not_found")
		}
		if err != nil {
			return nil, store.Course{}, fmt.Errorf("internal_error")
		}
		return nil, course, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_course_classes",
		Description: "按课程代码(全局稳定,来自 search_courses 结果的 course_code)获取一门课的全部教学班:不同老师、不同学期的班一次返回,含活水评分摘要。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input courseCodeInput) (*mcp.CallToolResult, store.CourseGroup, error) {
		group, err := s.ListCourseClassesByCode(ctx, input.CourseCode)
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.CourseGroup{}, fmt.Errorf("not_found")
		}
		if err != nil {
			return nil, store.CourseGroup{}, fmt.Errorf("internal_error")
		}
		return nil, group, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_course_reviews",
		Description: "获取活水(app.huoshui.org)上某门课程的学生评分细项与匿名评价。object_id 来自 get_course / search_courses 结果里的 huoshui.objectId。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input huoshuiIDInput) (*mcp.CallToolResult, store.HuoshuiCourseDetail, error) {
		detail, err := s.GetHuoshuiCourse(ctx, input.ObjectID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.HuoshuiCourseDetail{}, fmt.Errorf("not_found")
		}
		if err != nil {
			return nil, store.HuoshuiCourseDetail{}, fmt.Errorf("internal_error")
		}
		return nil, detail, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "dataset_meta",
		Description: "查看数据集概况:教师/课程/活水评价数量、开课学期、数据更新时间。",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, store.DatasetMeta, error) {
		return nil, s.Meta(), nil
	})

	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

func teacherFilterFromMCP(input searchTeachersInput) (store.TeacherFilter, error) {
	page, pageSize, err := mcpPagination(input.Page, input.PageSize)
	if err != nil {
		return store.TeacherFilter{}, err
	}
	search, err := mcpBoundedString(input.Search, 100, "search_too_long")
	if err != nil {
		return store.TeacherFilter{}, err
	}
	college, err := mcpBoundedString(input.College, 100, "college_too_long")
	if err != nil {
		return store.TeacherFilter{}, err
	}
	initial := strings.ToLower(strings.TrimSpace(input.Initial))
	if len(initial) > 1 {
		return store.TeacherFilter{}, fmt.Errorf("invalid_initial")
	}
	return store.TeacherFilter{Page: page, PageSize: pageSize, Search: search, Initial: initial, College: college}, nil
}

func courseFilterFromMCP(input searchCoursesInput) (store.CourseFilter, error) {
	page, pageSize, err := mcpPagination(input.Page, input.PageSize)
	if err != nil {
		return store.CourseFilter{}, err
	}
	search, err := mcpBoundedString(input.Search, 100, "search_too_long")
	if err != nil {
		return store.CourseFilter{}, err
	}
	campus, err := mcpBoundedString(input.Campus, 20, "invalid_campus")
	if err != nil {
		return store.CourseFilter{}, err
	}
	weekday, err := mcpBoundedString(input.Weekday, 10, "invalid_weekday")
	if err != nil {
		return store.CourseFilter{}, err
	}
	college, err := mcpBoundedString(input.College, 100, "college_too_long")
	if err != nil {
		return store.CourseFilter{}, err
	}
	category, err := mcpBoundedString(input.Category, 50, "category_too_long")
	if err != nil {
		return store.CourseFilter{}, err
	}
	return store.CourseFilter{Page: page, PageSize: pageSize, Search: search, Campus: campus, Weekday: weekday, College: college, Category: category}, nil
}

func mcpPagination(page, pageSize int) (int, int, error) {
	if page < 0 || page > 100000 || pageSize < 0 || pageSize > 100 {
		return 0, 0, fmt.Errorf("invalid_pagination")
	}
	if page == 0 {
		page = 1
	}
	if pageSize == 0 {
		pageSize = 24
	}
	return page, pageSize, nil
}

func mcpBoundedString(value string, maxRunes int, code string) (string, error) {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > maxRunes {
		return "", fmt.Errorf("%s", code)
	}
	return value, nil
}
