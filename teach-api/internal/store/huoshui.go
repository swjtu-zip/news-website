package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// HuoshuiRef is the rating summary attached to a registrar course when a
// huoshui course with the same normalized (name, teacher) exists.
type HuoshuiRef struct {
	ObjectID          string  `json:"objectId"`
	ReviewCount       int     `json:"reviewCount"`
	RateOverall       float64 `json:"rateOverall"`
	Rate1             float64 `json:"rate1"`
	Rate2             float64 `json:"rate2"`
	Rate3             float64 `json:"rate3"`
	AttendanceOverall float64 `json:"attendanceOverall"`
	ExamOverall       float64 `json:"examOverall"`
	BirdOverall       float64 `json:"birdOverall"`
}

// HuoshuiCourseSummary is one row of the standalone huoshui course list.
type HuoshuiCourseSummary struct {
	ObjectID          string  `json:"objectId"`
	Name              string  `json:"name"`
	Prof              string  `json:"prof"`
	Dept              string  `json:"dept,omitempty"`
	Position          string  `json:"position,omitempty"`
	ReviewCount       int     `json:"reviewCount"`
	RateOverall       float64 `json:"rateOverall"`
	Rate1             float64 `json:"rate1"`
	Rate2             float64 `json:"rate2"`
	Rate3             float64 `json:"rate3"`
	AttendanceOverall float64 `json:"attendanceOverall"`
	HomeworkOverall   float64 `json:"homeworkOverall"`
	ExamOverall       float64 `json:"examOverall"`
	BirdOverall       float64 `json:"birdOverall"`
}

// HuoshuiReview is one anonymous student review. Comment and tag fields are
// user-generated text; clients must escape them when rendering.
type HuoshuiReview struct {
	ObjectID      string  `json:"objectId"`
	Comment       string  `json:"comment"`
	RatingOverall float64 `json:"ratingOverall"`
	UpVote        int     `json:"upVote"`
	ExamInfo      string  `json:"examInfo,omitempty"`
	Attendance    string  `json:"attendance,omitempty"`
	Bird          string  `json:"bird,omitempty"`
	Homework      string  `json:"homework,omitempty"`
	AuthorDept    string  `json:"authorDept,omitempty"`
	AuthorYear    string  `json:"authorYear,omitempty"`
	CreatedAt     string  `json:"createdAt,omitempty"`
}

// HuoshuiCourseDetail carries a huoshui course plus its top reviews.
type HuoshuiCourseDetail struct {
	HuoshuiCourseSummary
	AttendanceCount int             `json:"attendanceCount"`
	HomeworkCount   int             `json:"homeworkCount"`
	ExamCount       int             `json:"examCount"`
	BirdCount       int             `json:"birdCount"`
	UpdatedAt       string          `json:"updatedAt,omitempty"`
	Reviews         []HuoshuiReview `json:"reviews"`
}

// HuoshuiMeta describes the imported huoshui dataset for the standalone view.
type HuoshuiMeta struct {
	Version     string   `json:"version,omitempty"`
	ImportedAt  string   `json:"importedAt,omitempty"`
	CourseCount int      `json:"courseCount"`
	ReviewCount int      `json:"reviewCount"`
	MatchCount  int      `json:"matchCount"`
	Depts       []string `json:"depts"`
}

type HuoshuiFilter struct {
	Page     int
	PageSize int
	Search   string
	Dept     string
	Sort     string // reviews | rating | rate3
}

type HuoshuiCoursePage struct {
	Data       []HuoshuiCourseSummary `json:"data"`
	Pagination Pagination             `json:"pagination"`
}

// HuoshuiImportStats reports what one import wrote, for logging.
type HuoshuiImportStats struct {
	Courses int
	Reviews int
	Matches int
}

// huoshuiCourseJSON mirrors one entry of the upstream courses.json (Chinese
// keys, produced by swjtu-zip/huoshui-scraper).
type huoshuiCourseJSON struct {
	ObjectID        string  `json:"课程ID"`
	Name            string  `json:"课程名称"`
	Prof            string  `json:"授课教师"`
	Dept            string  `json:"所属院系"`
	Position        string  `json:"教师职称"`
	ReviewCount     int     `json:"评价数量"`
	RateOverall     float64 `json:"综合评分"`
	Rate1           float64 `json:"课程质量"`
	Rate2           float64 `json:"作业多少"`
	Rate3           float64 `json:"给分高低"`
	AttendanceCount int     `json:"点名评价数"`
	Attendance      float64 `json:"点名频率"`
	HomeworkCount   int     `json:"作业评价数"`
	Homework        float64 `json:"作业量"`
	ExamCount       int     `json:"考试评价数"`
	Exam            float64 `json:"考试难度"`
	BirdCount       int     `json:"水课评价数"`
	Bird            float64 `json:"水课程度"`
	UpdatedAt       string  `json:"更新时间"`
}

// huoshuiReviewJSON mirrors one entry of the upstream reviews.json.
type huoshuiReviewJSON struct {
	ObjectID      string          `json:"评价ID"`
	CourseName    string          `json:"课程名称"`
	ProfName      string          `json:"授课教师"`
	Comment       string          `json:"评价内容"`
	RatingOverall float64         `json:"综合评分"`
	UpVote        int             `json:"点赞数"`
	ExamInfo      string          `json:"考试信息"`
	Attendance    string          `json:"点名情况"`
	Bird          string          `json:"水课程度"`
	Homework      string          `json:"作业情况"`
	AuthorDept    string          `json:"评价者院系"`
	AuthorYear    json.RawMessage `json:"评价者年级"`
	CreatedAt     string          `json:"评价时间"`
}

// ImportHuoshuiBytes replaces the huoshui tables with the upstream JSON
// snapshots and recomputes the registrar-course matches in one transaction.
func (s *Store) ImportHuoshuiBytes(ctx context.Context, coursesJSON, reviewsJSON []byte) (HuoshuiImportStats, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var stats HuoshuiImportStats
	var courses []huoshuiCourseJSON
	if err := json.Unmarshal(coursesJSON, &courses); err != nil {
		return stats, fmt.Errorf("decode huoshui courses: %w", err)
	}
	if len(courses) == 0 {
		return stats, errors.New("huoshui courses snapshot is empty")
	}
	var reviews []huoshuiReviewJSON
	if err := json.Unmarshal(reviewsJSON, &reviews); err != nil {
		return stats, fmt.Errorf("decode huoshui reviews: %w", err)
	}

	coursesDigest := sha256.Sum256(coursesJSON)
	reviewsDigest := sha256.Sum256(reviewsJSON)
	versionDigest := sha256.Sum256(append(coursesDigest[:], reviewsDigest[:]...))
	version := hex.EncodeToString(versionDigest[:])
	importedAt := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("begin huoshui import: %w", err)
	}
	rollback := func() {
		_ = tx.Rollback()
	}
	for _, table := range []string{"huoshui_match", "huoshui_reviews", "huoshui_courses"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			rollback()
			return stats, fmt.Errorf("replace %s: %w", table, err)
		}
	}

	courseStatement, err := tx.PrepareContext(ctx, `
INSERT INTO huoshui_courses (
	object_id, name, prof, dept, position, review_count,
	rate_overall, rate1, rate2, rate3,
	attendance_overall, attendance_count, homework_overall, homework_count,
	exam_overall, exam_count, bird_overall, bird_count, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		rollback()
		return stats, fmt.Errorf("prepare huoshui course import: %w", err)
	}
	seenCourses := make(map[string]struct{}, len(courses))
	for index, course := range courses {
		id := strings.TrimSpace(course.ObjectID)
		name := strings.TrimSpace(course.Name)
		prof := strings.TrimSpace(course.Prof)
		if id == "" || name == "" || prof == "" {
			continue
		}
		if _, exists := seenCourses[id]; exists {
			continue
		}
		seenCourses[id] = struct{}{}
		if _, err := courseStatement.ExecContext(ctx,
			id, name, prof, strings.TrimSpace(course.Dept), strings.TrimSpace(course.Position),
			course.ReviewCount, course.RateOverall, course.Rate1, course.Rate2, course.Rate3,
			course.Attendance, course.AttendanceCount, course.Homework, course.HomeworkCount,
			course.Exam, course.ExamCount, course.Bird, course.BirdCount,
			strings.TrimSpace(course.UpdatedAt),
		); err != nil {
			_ = courseStatement.Close()
			rollback()
			return stats, fmt.Errorf("insert huoshui course at index %d: %w", index, err)
		}
	}
	if err := courseStatement.Close(); err != nil {
		rollback()
		return stats, fmt.Errorf("close huoshui course import: %w", err)
	}

	reviewStatement, err := tx.PrepareContext(ctx, `
INSERT INTO huoshui_reviews (
	object_id, course_name, prof_name, comment, rating_overall, up_vote,
	exam_info, attendance, bird, homework, author_dept, author_year, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		rollback()
		return stats, fmt.Errorf("prepare huoshui review import: %w", err)
	}
	seenReviews := make(map[string]struct{}, len(reviews))
	for _, review := range reviews {
		id := strings.TrimSpace(review.ObjectID)
		if id == "" {
			continue
		}
		if _, exists := seenReviews[id]; exists {
			continue
		}
		seenReviews[id] = struct{}{}
		if _, err := reviewStatement.ExecContext(ctx,
			id, strings.TrimSpace(review.CourseName), strings.TrimSpace(review.ProfName),
			review.Comment, review.RatingOverall, review.UpVote,
			strings.TrimSpace(review.ExamInfo), strings.TrimSpace(review.Attendance),
			strings.TrimSpace(review.Bird), strings.TrimSpace(review.Homework),
			strings.TrimSpace(review.AuthorDept), rawJSONText(review.AuthorYear),
			strings.TrimSpace(review.CreatedAt),
		); err != nil {
			_ = reviewStatement.Close()
			rollback()
			return stats, fmt.Errorf("insert huoshui review %q: %w", id, err)
		}
	}
	if err := reviewStatement.Close(); err != nil {
		rollback()
		return stats, fmt.Errorf("close huoshui review import: %w", err)
	}

	matches, err := rebuildHuoshuiMatches(ctx, tx)
	if err != nil {
		rollback()
		return stats, err
	}

	stats = HuoshuiImportStats{Courses: len(seenCourses), Reviews: len(seenReviews), Matches: matches}
	for key, value := range map[string]string{
		"huoshui_version":        version,
		"huoshui_courses_sha256": hex.EncodeToString(coursesDigest[:]),
		"huoshui_reviews_sha256": hex.EncodeToString(reviewsDigest[:]),
		"huoshui_imported_at":    importedAt,
		"huoshui_course_count":   strconv.Itoa(stats.Courses),
		"huoshui_review_count":   strconv.Itoa(stats.Reviews),
		"huoshui_match_count":    strconv.Itoa(stats.Matches),
		"imported_at":            importedAt,
	} {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO dataset_meta (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			rollback()
			return stats, fmt.Errorf("write huoshui metadata: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		rollback()
		return stats, fmt.Errorf("commit huoshui import: %w", err)
	}
	if err := s.loadMeta(ctx); err != nil {
		return stats, fmt.Errorf("reload metadata: %w", err)
	}
	return stats, nil
}

// rebuildHuoshuiMatches links every registrar course to the huoshui course
// with the same normalized (name, teacher), preferring the highest review
// count when several huoshui courses match.
func rebuildHuoshuiMatches(ctx context.Context, tx *sql.Tx) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT object_id, name, prof, review_count FROM huoshui_courses`)
	if err != nil {
		return 0, fmt.Errorf("load huoshui courses for matching: %w", err)
	}
	best := make(map[string]string)
	bestCount := make(map[string]int)
	for rows.Next() {
		var id, name, prof string
		var reviewCount int
		if err := rows.Scan(&id, &name, &prof, &reviewCount); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan huoshui course for matching: %w", err)
		}
		key := matchKey(name, prof)
		if key == "::" {
			continue
		}
		if current, ok := best[key]; !ok || reviewCount > bestCount[key] || (reviewCount == bestCount[key] && id < current) {
			best[key] = id
			bestCount[key] = reviewCount
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("iterate huoshui courses for matching: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close huoshui course matching rows: %w", err)
	}

	registrarRows, err := tx.QueryContext(ctx, `SELECT id, course_name, teacher_name FROM courses`)
	if err != nil {
		return 0, fmt.Errorf("load registrar courses for matching: %w", err)
	}
	type matchRow struct {
		registrarID string
		huoshuiID   string
	}
	matches := make([]matchRow, 0, 256)
	for registrarRows.Next() {
		var id, name, teacher string
		if err := registrarRows.Scan(&id, &name, &teacher); err != nil {
			_ = registrarRows.Close()
			return 0, fmt.Errorf("scan registrar course for matching: %w", err)
		}
		if huoshuiID, ok := best[matchKey(name, teacher)]; ok {
			matches = append(matches, matchRow{registrarID: id, huoshuiID: huoshuiID})
		}
	}
	if err := registrarRows.Err(); err != nil {
		_ = registrarRows.Close()
		return 0, fmt.Errorf("iterate registrar courses for matching: %w", err)
	}
	if err := registrarRows.Close(); err != nil {
		return 0, fmt.Errorf("close registrar course matching rows: %w", err)
	}

	statement, err := tx.PrepareContext(ctx, `
INSERT INTO huoshui_match (registrar_course_id, huoshui_course_id) VALUES (?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare huoshui match import: %w", err)
	}
	for _, match := range matches {
		if _, err := statement.ExecContext(ctx, match.registrarID, match.huoshuiID); err != nil {
			_ = statement.Close()
			return 0, fmt.Errorf("insert huoshui match %q: %w", match.registrarID, err)
		}
	}
	if err := statement.Close(); err != nil {
		return 0, fmt.Errorf("close huoshui match import: %w", err)
	}
	return len(matches), nil
}

// matchKey normalizes a (course name, teacher name) pair: trim, unify
// full-width and half-width whitespace, then drop all spaces.
func matchKey(name, teacher string) string {
	normalize := func(value string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, strings.TrimSpace(value))
	}
	return normalize(name) + "::" + normalize(teacher)
}

// rawJSONText renders a JSON scalar (the upstream 评价者年级 is usually a
// number like 2024 but could be a string) as plain text.
func rawJSONText(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	if unquoted, err := strconv.Unquote(text); err == nil {
		return unquoted
	}
	if text == "null" {
		return ""
	}
	return text
}

// HuoshuiSHA256 returns the file hashes recorded by the last huoshui import,
// so callers can skip re-importing unchanged snapshots.
func (s *Store) HuoshuiSHA256(ctx context.Context) (coursesSHA, reviewsSHA string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT key, value FROM dataset_meta WHERE key IN ('huoshui_courses_sha256', 'huoshui_reviews_sha256')`)
	if err != nil {
		return "", "", fmt.Errorf("read huoshui hashes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return "", "", fmt.Errorf("scan huoshui hashes: %w", err)
		}
		switch key {
		case "huoshui_courses_sha256":
			coursesSHA = value
		case "huoshui_reviews_sha256":
			reviewsSHA = value
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", fmt.Errorf("iterate huoshui hashes: %w", err)
	}
	return coursesSHA, reviewsSHA, nil
}

// attachHuoshuiSummaries fills the huoshui rating summary of a course list
// page in one join query.
func (s *Store) attachHuoshuiSummaries(ctx context.Context, courses []CourseSummary) error {
	if len(courses) == 0 {
		return nil
	}
	ids := make([]string, 0, len(courses))
	for _, course := range courses {
		ids = append(ids, course.ID)
	}
	refs, err := s.huoshuiRefs(ctx, ids)
	if err != nil {
		return err
	}
	for index := range courses {
		if ref, ok := refs[courses[index].ID]; ok {
			refCopy := ref
			courses[index].Huoshui = &refCopy
		}
	}
	return nil
}

func (s *Store) attachHuoshui(ctx context.Context, course *Course) error {
	refs, err := s.huoshuiRefs(ctx, []string{course.ID})
	if err != nil {
		return err
	}
	if ref, ok := refs[course.ID]; ok {
		refCopy := ref
		course.Huoshui = &refCopy
	}
	return nil
}

func (s *Store) huoshuiRefs(ctx context.Context, registrarIDs []string) (map[string]HuoshuiRef, error) {
	refs := make(map[string]HuoshuiRef, len(registrarIDs))
	if len(registrarIDs) == 0 {
		return refs, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(registrarIDs)), ",")
	args := make([]any, 0, len(registrarIDs))
	for _, id := range registrarIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.registrar_course_id, h.object_id, h.review_count, h.rate_overall,
       h.rate1, h.rate2, h.rate3, h.attendance_overall, h.exam_overall, h.bird_overall
FROM huoshui_match m
JOIN huoshui_courses h ON h.object_id = m.huoshui_course_id
WHERE m.registrar_course_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("load huoshui refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var registrarID string
		var ref HuoshuiRef
		if err := rows.Scan(
			&registrarID, &ref.ObjectID, &ref.ReviewCount, &ref.RateOverall,
			&ref.Rate1, &ref.Rate2, &ref.Rate3, &ref.AttendanceOverall, &ref.ExamOverall, &ref.BirdOverall,
		); err != nil {
			return nil, fmt.Errorf("scan huoshui ref: %w", err)
		}
		refs[registrarID] = ref
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate huoshui refs: %w", err)
	}
	return refs, nil
}

// ListHuoshuiCourses pages the standalone huoshui rating list. rating and
// rate3 sorts only include courses with at least three reviews, matching the
// upstream site's own ranking rule.
func (s *Store) ListHuoshuiCourses(ctx context.Context, filter HuoshuiFilter) (HuoshuiCoursePage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	page := filter.Page
	if page < defaultPage {
		page = defaultPage
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}

	conditions := []string{"1 = 1"}
	args := make([]any, 0, 4)
	if search := sanitizeQuery(filter.Search); search != "" {
		pattern := likePattern(search)
		conditions = append(conditions, `(name LIKE ? ESCAPE '\' OR prof LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern)
	}
	if dept := sanitizeQuery(filter.Dept); dept != "" {
		conditions = append(conditions, `dept = ?`)
		args = append(args, dept)
	}
	order := `review_count DESC, name COLLATE NOCASE ASC, object_id ASC`
	switch filter.Sort {
	case "rating":
		conditions = append(conditions, `review_count >= 3`)
		order = `rate_overall DESC, review_count DESC, object_id ASC`
	case "rate3":
		conditions = append(conditions, `review_count >= 3`)
		order = `rate3 DESC, review_count DESC, object_id ASC`
	}
	where := strings.Join(conditions, " AND ")

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM huoshui_courses WHERE `+where, args...).Scan(&total); err != nil {
		return HuoshuiCoursePage{}, fmt.Errorf("count huoshui courses: %w", err)
	}
	offset := (page - 1) * pageSize
	rows, err := s.db.QueryContext(ctx, `
SELECT object_id, name, prof, dept, position, review_count,
       rate_overall, rate1, rate2, rate3,
       attendance_overall, homework_overall, exam_overall, bird_overall
FROM huoshui_courses
WHERE `+where+`
ORDER BY `+order+`
LIMIT ? OFFSET ?`, append(args, pageSize, offset)...)
	if err != nil {
		return HuoshuiCoursePage{}, fmt.Errorf("list huoshui courses: %w", err)
	}
	defer rows.Close()
	data := make([]HuoshuiCourseSummary, 0, pageSize)
	for rows.Next() {
		course, err := scanHuoshuiSummary(rows)
		if err != nil {
			return HuoshuiCoursePage{}, fmt.Errorf("scan huoshui course: %w", err)
		}
		data = append(data, course)
	}
	if err := rows.Err(); err != nil {
		return HuoshuiCoursePage{}, fmt.Errorf("iterate huoshui courses: %w", err)
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	return HuoshuiCoursePage{
		Data: data,
		Pagination: Pagination{
			Page: page, PageSize: pageSize, Total: total, TotalPages: totalPages,
		},
	}, nil
}

// GetHuoshuiCourse returns one huoshui course with its top reviews, most
// up-voted first.
func (s *Store) GetHuoshuiCourse(ctx context.Context, id string) (HuoshuiCourseDetail, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return HuoshuiCourseDetail{}, ErrNotFound
	}
	var detail HuoshuiCourseDetail
	row := s.db.QueryRowContext(ctx, `
SELECT object_id, name, prof, dept, position, review_count,
       rate_overall, rate1, rate2, rate3,
       attendance_overall, homework_overall, exam_overall, bird_overall,
       attendance_count, homework_count, exam_count, bird_count, updated_at
FROM huoshui_courses WHERE object_id = ?`, id)
	err := row.Scan(
		&detail.ObjectID, &detail.Name, &detail.Prof, &detail.Dept, &detail.Position, &detail.ReviewCount,
		&detail.RateOverall, &detail.Rate1, &detail.Rate2, &detail.Rate3,
		&detail.AttendanceOverall, &detail.HomeworkOverall, &detail.ExamOverall, &detail.BirdOverall,
		&detail.AttendanceCount, &detail.HomeworkCount, &detail.ExamCount, &detail.BirdCount, &detail.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return HuoshuiCourseDetail{}, ErrNotFound
	}
	if err != nil {
		return HuoshuiCourseDetail{}, fmt.Errorf("get huoshui course: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT object_id, comment, rating_overall, up_vote,
       exam_info, attendance, bird, homework, author_dept, author_year, created_at
FROM huoshui_reviews
WHERE course_name = ? AND prof_name = ?
ORDER BY up_vote DESC, created_at DESC
LIMIT 100`, detail.Name, detail.Prof)
	if err != nil {
		return HuoshuiCourseDetail{}, fmt.Errorf("list huoshui reviews: %w", err)
	}
	defer rows.Close()
	detail.Reviews = make([]HuoshuiReview, 0, 16)
	for rows.Next() {
		var review HuoshuiReview
		if err := rows.Scan(
			&review.ObjectID, &review.Comment, &review.RatingOverall, &review.UpVote,
			&review.ExamInfo, &review.Attendance, &review.Bird, &review.Homework,
			&review.AuthorDept, &review.AuthorYear, &review.CreatedAt,
		); err != nil {
			return HuoshuiCourseDetail{}, fmt.Errorf("scan huoshui review: %w", err)
		}
		detail.Reviews = append(detail.Reviews, review)
	}
	if err := rows.Err(); err != nil {
		return HuoshuiCourseDetail{}, fmt.Errorf("iterate huoshui reviews: %w", err)
	}
	return detail, nil
}

// ListHuoshuiMeta summarizes the imported huoshui dataset, including the
// department list used by the standalone view's filter.
func (s *Store) ListHuoshuiMeta(ctx context.Context) (HuoshuiMeta, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	meta := s.Meta()
	result := HuoshuiMeta{
		Version:     meta.HuoshuiVersion,
		ImportedAt:  meta.HuoshuiImportedAt,
		CourseCount: meta.HuoshuiCourseCount,
		ReviewCount: meta.HuoshuiReviewCount,
		MatchCount:  meta.HuoshuiMatchCount,
		Depts:       []string{},
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT dept, COUNT(*) AS courses FROM huoshui_courses WHERE dept <> ''
GROUP BY dept ORDER BY courses DESC, dept ASC`)
	if err != nil {
		return result, fmt.Errorf("list huoshui depts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var dept string
		var count int
		if err := rows.Scan(&dept, &count); err != nil {
			return result, fmt.Errorf("scan huoshui dept: %w", err)
		}
		result.Depts = append(result.Depts, dept)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("iterate huoshui depts: %w", err)
	}
	return result, nil
}

func scanHuoshuiSummary(row rowScanner) (HuoshuiCourseSummary, error) {
	var course HuoshuiCourseSummary
	if err := row.Scan(
		&course.ObjectID, &course.Name, &course.Prof, &course.Dept, &course.Position,
		&course.ReviewCount, &course.RateOverall, &course.Rate1, &course.Rate2, &course.Rate3,
		&course.AttendanceOverall, &course.HomeworkOverall, &course.ExamOverall, &course.BirdOverall,
	); err != nil {
		return HuoshuiCourseSummary{}, err
	}
	return course, nil
}
