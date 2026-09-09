package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func openHuoshuiTestStore(t *testing.T) *Store {
	t.Helper()
	database, err := Open(t.TempDir() + "/teach.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.ImportBytes(context.Background(), []byte(`{
"generated_at":"2026-09-07T14:32:03Z",
"source":{"site":"faculty.swjtu.edu.cn"},
"teachers":[{"name":"赵春明","profile_url":"https://faculty.swjtu.edu.cn/a/zh_cn/zhaochunming.html","status":"ok"}]
}`)); err != nil {
		t.Fatal(err)
	}
	if err := database.ImportCoursesBytes(context.Background(), []byte(`{
"schema_version":1,
"term":"2026-2027第1学期",
"courses":[
  {"id":"A0001","course_name":"概率论","teacher_name":"赵春明","campus":"犀浦"},
  {"id":"A0002","course_name":"概率论","teacher_name":"赵春明","campus":"九里"},
  {"id":"A0003","course_name":"高等数学","teacher_name":"符伟","campus":"犀浦"},
  {"id":"A0004","course_name":"冷门课程","teacher_name":"无名氏","campus":"犀浦"}
]}`)); err != nil {
		t.Fatal(err)
	}
	return database
}

func importHuoshuiFixture(t *testing.T, database *Store) HuoshuiImportStats {
	t.Helper()
	stats, err := database.ImportHuoshuiBytes(context.Background(),
		[]byte(`[
{"课程ID":"h1","课程名称":"概率论","授课教师":"赵春明","所属院系":"数学","教师职称":"讲师","评价数量":181,"综合评分":4.99,"课程质量":4.97,"作业多少":4.99,"给分高低":4.99,"点名评价数":76,"点名频率":1.99,"作业评价数":76,"作业量":2.92,"考试评价数":59,"考试难度":1.12,"水课评价数":75,"水课程度":1.36,"更新时间":"2026-07-28T02:08:25Z"},
{"课程ID":"h2","课程名称":"概率论","授课教师":"赵 春明","所属院系":"数学","评价数量":5,"综合评分":3.5,"课程质量":3.5,"作业多少":3.5,"给分高低":3.5},
{"课程ID":"h3","课程名称":"高等数学","授课教师":"符伟","所属院系":"数学","评价数量":2,"综合评分":5,"课程质量":5,"作业多少":5,"给分高低":5}
]`),
		[]byte(`[
{"评价ID":"r1","课程名称":"概率论","授课教师":"赵春明","评价内容":"老师讲得好","综合评分":5,"点赞数":42,"考试信息":"划重点、开卷","点名情况":"偶尔点名","水课程度":"","作业情况":"作业多","评价者院系":"数学","评价者年级":2024,"评价时间":"2026-09-08 07:15"},
{"评价ID":"r2","课程名称":"概率论","授课教师":"赵春明","评价内容":"一般","综合评分":3,"点赞数":2,"考试信息":"","点名情况":"","水课程度":"","作业情况":"","评价者院系":"计算机","评价者年级":"2023","评价时间":"2026-01-01 10:00"},
{"评价ID":"r3","课程名称":"高等数学","授课教师":"符伟","评价内容":"满分推荐","综合评分":5,"点赞数":7,"考试信息":"闭卷","点名情况":"","水课程度":"","作业情况":"","评价者院系":"","评价者年级":null,"评价时间":"2025-12-01 08:00"}
]`))
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

func TestImportHuoshuiMatchesRegistrarCourses(t *testing.T) {
	database := openHuoshuiTestStore(t)
	stats := importHuoshuiFixture(t, database)
	if stats.Courses != 3 || stats.Reviews != 3 {
		t.Fatalf("unexpected import stats: %+v", stats)
	}
	// A0001 and A0002 are two 教学班 of 概率论/赵春明; both must match, and the
	// ambiguous huoshui candidates (h1 vs h2 after space normalization) must
	// resolve to the one with more reviews. A0004 has no match.
	if stats.Matches != 3 {
		t.Fatalf("matches = %d, want 3", stats.Matches)
	}

	page, err := database.ListCourses(context.Background(), CourseFilter{Page: 1, PageSize: 24})
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]CourseSummary, len(page.Data))
	for _, course := range page.Data {
		byID[course.ID] = course
	}
	for _, id := range []string{"A0001", "A0002"} {
		ref := byID[id].Huoshui
		if ref == nil {
			t.Fatalf("%s missing huoshui ref", id)
		}
		if ref.ObjectID != "h1" || ref.ReviewCount != 181 || ref.RateOverall != 4.99 {
			t.Fatalf("%s huoshui ref = %+v, want h1/181/4.99", id, ref)
		}
	}
	if byID["A0003"].Huoshui == nil || byID["A0003"].Huoshui.ObjectID != "h3" {
		t.Fatalf("A0003 huoshui ref = %+v", byID["A0003"].Huoshui)
	}
	if byID["A0004"].Huoshui != nil {
		t.Fatalf("A0004 must stay unmatched, got %+v", byID["A0004"].Huoshui)
	}

	detail, err := database.GetCourse(context.Background(), "A0001")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Huoshui == nil || detail.Huoshui.ObjectID != "h1" {
		t.Fatalf("detail huoshui ref = %+v", detail.Huoshui)
	}

	meta := database.Meta()
	if meta.HuoshuiCourseCount != 3 || meta.HuoshuiReviewCount != 3 || meta.HuoshuiMatchCount != 3 || meta.HuoshuiVersion == "" {
		t.Fatalf("unexpected meta: %+v", meta)
	}
}

func TestHuoshuiSHA256RoundTrip(t *testing.T) {
	database := openHuoshuiTestStore(t)
	courses := []byte(`[{"课程ID":"h1","课程名称":"概率论","授课教师":"赵春明","评价数量":1}]`)
	reviews := []byte(`[]`)
	if _, err := database.ImportHuoshuiBytes(context.Background(), courses, reviews); err != nil {
		t.Fatal(err)
	}
	coursesSHA, reviewsSHA, err := database.HuoshuiSHA256(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	coursesSum := sha256.Sum256(courses)
	reviewsSum := sha256.Sum256(reviews)
	if coursesSHA != hex.EncodeToString(coursesSum[:]) || reviewsSHA != hex.EncodeToString(reviewsSum[:]) {
		t.Fatalf("hashes = %s/%s", coursesSHA, reviewsSHA)
	}
}

func TestListHuoshuiCoursesFiltersAndSorts(t *testing.T) {
	database := openHuoshuiTestStore(t)
	importHuoshuiFixture(t, database)

	all, err := database.ListHuoshuiCourses(context.Background(), HuoshuiFilter{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if all.Pagination.Total != 3 || all.Data[0].ObjectID != "h1" {
		t.Fatalf("default list: %+v", all)
	}

	searched, err := database.ListHuoshuiCourses(context.Background(), HuoshuiFilter{Page: 1, PageSize: 50, Search: "春明"})
	if err != nil {
		t.Fatal(err)
	}
	if searched.Pagination.Total != 2 {
		t.Fatalf("search by prof: total = %d, want 2", searched.Pagination.Total)
	}

	rated, err := database.ListHuoshuiCourses(context.Background(), HuoshuiFilter{Page: 1, PageSize: 50, Sort: "rating"})
	if err != nil {
		t.Fatal(err)
	}
	// h3 (2 reviews) must be filtered out by the review_count >= 3 rule.
	if rated.Pagination.Total != 2 || rated.Data[0].ObjectID != "h1" || rated.Data[1].ObjectID != "h2" {
		t.Fatalf("rating sort: %+v", rated)
	}

	rate3, err := database.ListHuoshuiCourses(context.Background(), HuoshuiFilter{Page: 1, PageSize: 50, Sort: "rate3"})
	if err != nil {
		t.Fatal(err)
	}
	if rate3.Pagination.Total != 2 || rate3.Data[0].ObjectID != "h1" {
		t.Fatalf("rate3 sort: %+v", rate3)
	}

	byDept, err := database.ListHuoshuiCourses(context.Background(), HuoshuiFilter{Page: 1, PageSize: 50, Dept: "不存在院系"})
	if err != nil {
		t.Fatal(err)
	}
	if byDept.Pagination.Total != 0 {
		t.Fatalf("dept filter: total = %d, want 0", byDept.Pagination.Total)
	}

	paged, err := database.ListHuoshuiCourses(context.Background(), HuoshuiFilter{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if paged.Pagination.TotalPages != 2 || len(paged.Data) != 1 {
		t.Fatalf("pagination: %+v", paged.Pagination)
	}
}

func TestGetHuoshuiCourseDetail(t *testing.T) {
	database := openHuoshuiTestStore(t)
	importHuoshuiFixture(t, database)

	detail, err := database.GetHuoshuiCourse(context.Background(), "h1")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Name != "概率论" || detail.AttendanceCount != 76 || len(detail.Reviews) != 2 {
		t.Fatalf("detail: %+v", detail)
	}
	if detail.Reviews[0].ObjectID != "r1" || detail.Reviews[0].UpVote != 42 {
		t.Fatalf("reviews not sorted by up_vote: %+v", detail.Reviews)
	}
	if detail.Reviews[0].AuthorYear != "2024" || detail.Reviews[0].ExamInfo != "划重点、开卷" {
		t.Fatalf("review fields: %+v", detail.Reviews[0])
	}

	if _, err := database.GetHuoshuiCourse(context.Background(), "missing"); err != ErrNotFound {
		t.Fatalf("missing course err = %v", err)
	}

	meta, err := database.ListHuoshuiMeta(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if meta.CourseCount != 3 || meta.MatchCount != 3 || len(meta.Depts) != 1 || meta.Depts[0] != "数学" {
		t.Fatalf("huoshui meta: %+v", meta)
	}
}
