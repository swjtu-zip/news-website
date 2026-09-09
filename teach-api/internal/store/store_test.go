package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportListAndGet(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "nested", "teach.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	snapshot := Snapshot{
		SchemaVersion: 1,
		GeneratedAt:   "2026-09-07T14:32:03Z",
		Source: map[string]string{
			"site":     "faculty.swjtu.edu.cn",
			"base_url": "https://faculty.swjtu.edu.cn",
		},
		Stats: SnapshotStats{ListPages: 9},
		Teachers: []SnapshotTeacher{
			{
				Name: "张三", ProfileURL: "https://faculty.swjtu.edu.cn/a/zh_cn/zhangsan.html", Initial: "z",
				College: "计算机学院", Position: "教授", Introduction: "研究人工智能，email: zhang@example.edu.cn 电话 13800138000",
				Research: []string{"机器学习", "邮箱 zhang@example.edu.cn"}, Status: "ok",
			},
			{
				Name: "李四", ProfileURL: "https://faculty.swjtu.edu.cn/a/zh_cn/lisi.html", Initial: "l",
				College: "数学学院", Position: "副教授", PrimaryRole: "系副主任", EntryDate: "2020-07-13",
				Email: "lisi@example.edu.cn", Phone: "028-87654321", OfficeLocation: "犀浦校区", PostalCode: "611756",
				AvatarURL: "/images/lisi.jpg", Sections: []SnapshotSection{{Title: "教育经历", Content: "西南交通大学，博士。"}}, Status: "ok",
			},
			{
				Name: "王五", ProfileURL: "https://faculty.swjtu.edu.cn/a/zh_cn/wangwu.html", Initial: "w",
				College: "计算机学院", Position: "讲师", Status: "ok",
			},
		},
	}
	contents, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ImportBytes(context.Background(), contents); err != nil {
		t.Fatal(err)
	}

	meta := database.Meta()
	if meta.RecordCount != 3 || meta.ListPages != 9 || meta.Version == "" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	if !database.HasDataset() {
		t.Fatal("imported database should have a dataset")
	}

	page, err := database.ListTeachers(context.Background(), TeacherFilter{Page: 1, PageSize: 1, Search: "计算机"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Pagination.Total != 2 || page.Pagination.TotalPages != 2 || len(page.Data) != 1 {
		t.Fatalf("unexpected filtered page: %+v", page)
	}
	if page.Data[0].Name != "王五" && page.Data[0].Name != "张三" {
		t.Fatalf("unexpected teacher: %+v", page.Data[0])
	}
	if strings.Contains(page.Data[0].Introduction, "@") || strings.Contains(page.Data[0].Introduction, "13800138000") {
		t.Fatalf("contact data crossed public boundary: %+v", page.Data[0])
	}
	if len(page.Data[0].Research) != 1 || page.Data[0].Research[0] != "机器学习" {
		t.Fatalf("research contacts were not removed: %+v", page.Data[0].Research)
	}

	initialPage, err := database.ListTeachers(context.Background(), TeacherFilter{Initial: "L", PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if initialPage.Pagination.Total != 1 || initialPage.Data[0].Name != "李四" {
		t.Fatalf("initial filter failed: %+v", initialPage)
	}

	teacher, err := database.GetTeacher(context.Background(), initialPage.Data[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if teacher.ProfileURL == "" || teacher.Name != "李四" {
		t.Fatalf("unexpected detail: %+v", teacher)
	}
	if teacher.Email != "lisi@example.edu.cn" || teacher.Phone != "028-87654321" || teacher.OfficeLocation != "犀浦校区" || teacher.PostalCode != "611756" || teacher.PrimaryRole != "系副主任" || teacher.EntryDate != "2020-07-13" {
		t.Fatalf("public contact fields were not persisted: %+v", teacher)
	}
	if teacher.AvatarURL != "https://faculty.swjtu.edu.cn/images/lisi.jpg" || len(teacher.Sections) != 1 || teacher.Sections[0].Title != "教育经历" {
		t.Fatalf("public profile details were not persisted: %+v", teacher)
	}
	if _, err := database.GetTeacher(context.Background(), "missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestImportRejectsInvalidTeacher(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "teach.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	contents := []byte(`{"teachers":[{"name":"无链接","status":"ok"}]}`)
	if err := database.ImportBytes(context.Background(), contents); err == nil {
		t.Fatal("expected invalid snapshot to be rejected")
	}
}

func TestOpenExistingDatabaseLoadsMetadata(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "teach.db")
	database, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ImportBytes(context.Background(), []byte(`{
"generated_at":"2026-09-07T00:00:00Z",
"source":{"site":"example"},
"teachers":[{"name":"赵六","profile_url":"https://example.test/z.html","status":"ok"}]
}`)); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reopened.HasDataset() || reopened.Meta().RecordCount != 1 {
		t.Fatalf("metadata was not restored: %+v", reopened.Meta())
	}
	if _, err := os.Stat(databasePath); err != nil {
		t.Fatal(err)
	}
}

func TestImportCoursesAndQueries(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "teach.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	teachers := []byte(`{
"generated_at":"2026-09-07T00:00:00Z",
"source":{"site":"faculty.swjtu.edu.cn"},
"teachers":[
  {"name":"杨爱华","profile_url":"https://faculty.swjtu.edu.cn/a/zh_cn/yangaihua.html","status":"ok"},
  {"name":"王梅","profile_url":"https://faculty.swjtu.edu.cn/a/zh_cn/wangmei1.html","status":"ok"},
  {"name":"王梅","profile_url":"https://faculty.swjtu.edu.cn/b/zh_cn/wangmei2.html","status":"ok"}
]}`)
	if err := database.ImportBytes(context.Background(), teachers); err != nil {
		t.Fatal(err)
	}
	courses := []byte(`{
"schema_version":1,
"generated_at":"2026-09-09T00:00:00Z",
"term":"2026-2027第1学期",
"courses":[
  {"id":"B3919","term":"2026-2027第1学期","college":"本科生院","course_code":"CFGE000114","course_name":"女性成长","class_num":"1","teacher_name":"杨爱华","teacher_title":"研究员","credit":2,"hours_total":32,"hours_week":2,"weeks":"1-17","weekday":"星期三","periods":"11-12","campus":"犀浦","schedule_text":"1-17周 星期三 11-12节","assessment":"考试周","nature":"选修","category":"通识课","remark":"周三晚上"},
  {"id":"A0360","term":"2026-2027第1学期","college":"本科生院","course_code":"CFGE000415","course_name":"世界遗产与文明互鉴","class_num":"2","teacher_name":"王梅","teacher_title":"副教授","credit":2,"campus":"九里","weekday":"星期三","periods":"11-12","nature":"选修"},
  {"id":"B0001","term":"2026-2027第1学期","college":"数学学院","course_code":"MATH000001","course_name":"数学漫谈","class_num":"1","teacher_name":"目录外老师","credit":1,"campus":"犀浦","weekday":"星期一","periods":"1-2","nature":"选修"}
]}`)
	if err := database.ImportCoursesBytes(context.Background(), courses); err != nil {
		t.Fatal(err)
	}

	meta := database.Meta()
	if meta.CourseCount != 3 || meta.CoursesTerm != "2026-2027第1学期" || meta.CoursesVersion == "" {
		t.Fatalf("unexpected courses metadata: %+v", meta)
	}

	page, err := database.ListCourses(context.Background(), CourseFilter{Page: 1, PageSize: 10, Campus: "犀浦"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Pagination.Total != 2 || len(page.Data) != 2 {
		t.Fatalf("unexpected campus filter: %+v", page)
	}
	search, err := database.ListCourses(context.Background(), CourseFilter{Page: 1, PageSize: 10, Search: "杨爱华"})
	if err != nil {
		t.Fatal(err)
	}
	if search.Pagination.Total != 1 || search.Data[0].CourseName != "女性成长" {
		t.Fatalf("unexpected teacher search: %+v", search)
	}

	detail, err := database.GetCourse(context.Background(), "B3919")
	if err != nil {
		t.Fatal(err)
	}
	if detail.TeacherID == "" || detail.ScheduleText != "1-17周 星期三 11-12节" || detail.Credit != 2 {
		t.Fatalf("teacher was not linked or detail wrong: %+v", detail)
	}
	ambiguous, err := database.GetCourse(context.Background(), "A0360")
	if err != nil {
		t.Fatal(err)
	}
	if ambiguous.TeacherID != "" {
		t.Fatalf("ambiguous teacher name must stay unlinked: %+v", ambiguous)
	}
	outside, err := database.GetCourse(context.Background(), "B0001")
	if err != nil {
		t.Fatal(err)
	}
	if outside.TeacherID != "" {
		t.Fatalf("unknown teacher must stay unlinked: %+v", outside)
	}
	if _, err := database.GetCourse(context.Background(), "XXXX"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	teacherCourses, err := database.ListTeacherCourses(context.Background(), detail.TeacherID)
	if err != nil {
		t.Fatal(err)
	}
	if len(teacherCourses) != 1 || teacherCourses[0].ID != "B3919" {
		t.Fatalf("unexpected teacher courses: %+v", teacherCourses)
	}

	// 重新导入同一批课程应当完全替换旧数据。
	if err := database.ImportCoursesBytes(context.Background(), courses); err != nil {
		t.Fatal(err)
	}
	if database.Meta().CourseCount != 3 {
		t.Fatalf("reimport should replace, got %+v", database.Meta())
	}
}

func TestImportCoursesRejectsInvalid(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "teach.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.ImportCoursesBytes(context.Background(), []byte(`{"courses":[]}`)); err == nil {
		t.Fatal("expected empty snapshot to be rejected")
	}
	duplicate := []byte(`{"courses":[
  {"id":"B1","course_name":"甲","teacher_name":"张三"},
  {"id":"B1","course_name":"乙","teacher_name":"李四"}
]}`)
	if err := database.ImportCoursesBytes(context.Background(), duplicate); err == nil {
		t.Fatal("expected duplicate id to be rejected")
	}
	if database.Meta().CourseCount != 0 {
		t.Fatalf("failed import must not leave partial data: %+v", database.Meta())
	}
}
