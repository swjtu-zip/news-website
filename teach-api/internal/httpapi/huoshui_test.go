package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"teach-api/internal/store"
)

func openHuoshuiAPI(t *testing.T) http.Handler {
	t.Helper()
	database, err := store.Open(t.TempDir() + "/teach.db")
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
  {"id":"A0002","course_name":"冷门课程","teacher_name":"无名氏","campus":"九里"}
]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ImportHuoshuiBytes(context.Background(),
		[]byte(`[{"课程ID":"h1","课程名称":"概率论","授课教师":"赵春明","所属院系":"数学","评价数量":181,"综合评分":4.99,"课程质量":4.97,"作业多少":4.99,"给分高低":4.99}]`),
		[]byte(`[{"评价ID":"r1","课程名称":"概率论","授课教师":"赵春明","评价内容":"<script>alert(1)</script>","综合评分":5,"点赞数":42,"考试信息":"划重点","点名情况":"","水课程度":"","作业情况":"","评价者院系":"数学","评价者年级":2024,"评价时间":"2026-09-08 07:15"}]`)); err != nil {
		t.Fatal(err)
	}
	return New(database, Options{Logger: log.New(&bytes.Buffer{}, "", 0)}).Handler()
}

func TestHuoshuiEndpoints(t *testing.T) {
	handler := openHuoshuiAPI(t)

	metaRequest := httptest.NewRequest(http.MethodGet, "/api/v1/huoshui/meta", nil)
	metaResponse := httptest.NewRecorder()
	handler.ServeHTTP(metaResponse, metaRequest)
	if metaResponse.Code != http.StatusOK {
		t.Fatalf("meta status = %d", metaResponse.Code)
	}
	var metaPayload struct {
		Data store.HuoshuiMeta `json:"data"`
	}
	if err := json.Unmarshal(metaResponse.Body.Bytes(), &metaPayload); err != nil {
		t.Fatal(err)
	}
	if metaPayload.Data.CourseCount != 1 || metaPayload.Data.MatchCount != 1 ||
		len(metaPayload.Data.Depts) != 1 || metaPayload.Data.Depts[0] != "数学" {
		t.Fatalf("huoshui meta: %+v", metaPayload.Data)
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/huoshui/courses?search=%E8%B5%B5%E6%98%A5%E6%98%8E", nil)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listResponse.Code, listResponse.Body.String())
	}
	if listResponse.Header().Get("ETag") == "" || listResponse.Header().Get("Cache-Control") != publicCacheControl {
		t.Fatalf("missing cache validators: %v", listResponse.Header())
	}
	var page store.HuoshuiCoursePage
	if err := json.Unmarshal(listResponse.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Pagination.Total != 1 || page.Data[0].ObjectID != "h1" || page.Data[0].RateOverall != 4.99 {
		t.Fatalf("unexpected huoshui list: %+v", page)
	}

	badSort := httptest.NewRequest(http.MethodGet, "/api/v1/huoshui/courses?sort=bogus", nil)
	badSortResponse := httptest.NewRecorder()
	handler.ServeHTTP(badSortResponse, badSort)
	if badSortResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid sort status = %d", badSortResponse.Code)
	}

	detailRequest := httptest.NewRequest(http.MethodGet, "/api/v1/huoshui/courses/h1", nil)
	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status = %d", detailResponse.Code)
	}
	var detail struct {
		Data store.HuoshuiCourseDetail `json:"data"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Data.Reviews) != 1 || detail.Data.Reviews[0].UpVote != 42 {
		t.Fatalf("unexpected huoshui detail: %+v", detail.Data)
	}

	notFound := httptest.NewRequest(http.MethodGet, "/api/v1/huoshui/courses/missing", nil)
	notFoundResponse := httptest.NewRecorder()
	handler.ServeHTTP(notFoundResponse, notFound)
	if notFoundResponse.Code != http.StatusNotFound {
		t.Fatalf("missing huoshui course status = %d", notFoundResponse.Code)
	}
}

// TestRegistrarCoursesCarryHuoshuiRef checks the matched course exposes the
// rating summary while the unmatched course keeps an explicit null field.
func TestRegistrarCoursesCarryHuoshuiRef(t *testing.T) {
	handler := openHuoshuiAPI(t)

	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/courses", nil)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d", listResponse.Code)
	}
	body := listResponse.Body.String()
	if !strings.Contains(body, `"huoshui":{`) || !strings.Contains(body, `"objectId":"h1"`) {
		t.Fatalf("matched course missing huoshui ref: %s", body)
	}
	if !strings.Contains(body, `"huoshui":null`) {
		t.Fatalf("unmatched course missing explicit null: %s", body)
	}

	matchedDetail := httptest.NewRequest(http.MethodGet, "/api/v1/courses/A0001", nil)
	matchedResponse := httptest.NewRecorder()
	handler.ServeHTTP(matchedResponse, matchedDetail)
	if !strings.Contains(matchedResponse.Body.String(), `"reviewCount":181`) {
		t.Fatalf("matched detail missing huoshui: %s", matchedResponse.Body.String())
	}

	unmatchedDetail := httptest.NewRequest(http.MethodGet, "/api/v1/courses/A0002", nil)
	unmatchedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unmatchedResponse, unmatchedDetail)
	if !strings.Contains(unmatchedResponse.Body.String(), `"huoshui":null`) {
		t.Fatalf("unmatched detail missing null huoshui: %s", unmatchedResponse.Body.String())
	}
}
