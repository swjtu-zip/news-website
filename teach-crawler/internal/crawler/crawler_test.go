package crawler

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixtureFetcher struct {
	pages map[string][]byte
}

func (f fixtureFetcher) Fetch(_ context.Context, rawURL string) ([]byte, error) {
	if page, ok := f.pages[rawURL]; ok {
		return page, nil
	}
	return nil, os.ErrNotExist
}

func TestParseDirectoryFindsNamesCountsAndIgnoresExternalLinks(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "list-a.html"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := ParseDirectory(body, "https://faculty.swjtu.edu.cn/pyjslb.jsp?py=a")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries: %+v", len(entries), entries)
	}
	if entries[0].Name != "安博洋" || entries[0].DirectoryNum != 107 || entries[0].AvatarURL != "https://faculty.swjtu.edu.cn/anboyang.jpg" || !strings.Contains(entries[0].ProfileURL, "/anboyang/zh_CN/index.htm") {
		t.Fatalf("unexpected first entry: %+v", entries[0])
	}
	if entries[1].Name != "ADNAN YOUSAF" || entries[1].DirectoryNum != 11 {
		t.Fatalf("unexpected second entry: %+v", entries[1])
	}
}

func TestListingNameParsesThousandsSeparator(t *testing.T) {
	name, count := listingName("陈春谛 1,187")
	if name != "陈春谛" || count != 1187 {
		t.Fatalf("got name=%q count=%d", name, count)
	}
}

func TestSanitizePublicTextRemovesInlineContacts(t *testing.T) {
	got := sanitizePublicText("研究简介 secret@example.com，yz#swjtu.edu.cn，13800138000，028-87654321")
	if !strings.Contains(got, "研究简介") || strings.Contains(got, "secret@example.com") || strings.Contains(got, "yz#swjtu.edu.cn") || strings.Contains(got, "13800138000") || strings.Contains(got, "028-87654321") {
		t.Fatalf("unexpected sanitized text: %q", got)
	}
}

func TestSanitizePublicTextStopsAtEmailLabel(t *testing.T) {
	got := sanitizePublicText("公开简介。欢迎电子邮件联系：yz#swjtu.edu.cn (replace # with @)")
	if got != "公开简介。欢迎" {
		t.Fatalf("unexpected email-labelled text: %q", got)
	}
}

func TestParseContactResponse(t *testing.T) {
	got, err := parseContactResponse([]byte(`{"content":"<a href=\"mailto:test@example.com\">test@example.com</a>"}`))
	if err != nil || got != "test@example.com" {
		t.Fatalf("unexpected decoded contact: %q err=%v", got, err)
	}
	if got, err := parseContactResponse([]byte(`{"content":"611756"}`)); err != nil || got != "611756" {
		t.Fatalf("unexpected decoded postal code: %q err=%v", got, err)
	}
}

func TestDecodeEncryptedContacts(t *testing.T) {
	pageURL := "https://faculty.swjtu.edu.cn/test/zh_CN/index.htm"
	emailCipher := strings.Repeat("a", 128)
	postalCipher := strings.Repeat("b", 128)
	emailEndpoint, err := encryptedContactURL(pageURL, emailCipher, "8")
	if err != nil {
		t.Fatal(err)
	}
	postalEndpoint, err := encryptedContactURL(pageURL, postalCipher, "8")
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := url.Parse(emailEndpoint); err != nil || parsed.Query().Get("content") != emailCipher {
		t.Fatalf("encrypted content was not safely encoded: %s err=%v", emailEndpoint, err)
	}
	instance, err := New(Options{
		BaseURL: "https://faculty.swjtu.edu.cn", Letters: []string{"a"},
		Fetcher: fixtureFetcher{pages: map[string][]byte{
			emailEndpoint:  []byte(`{"content":"<a href=\"mailto:test@example.com\">test@example.com</a>"}`),
			postalEndpoint: []byte(`{"content":"611756"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile := ProfileData{Email: emailCipher, PostalCode: postalCipher}
	instance.decodeEncryptedContacts(context.Background(), pageURL, []byte(`var _tsites_com_view_mode_type_=8;`), &profile)
	if profile.Email != "test@example.com" || profile.PostalCode != "611756" {
		t.Fatalf("encrypted contacts were not decoded: %+v", profile)
	}
}

func TestParseProfileAllowlistsPublicFields(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "profile-anboyang.html"))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := ParseProfile(body)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "安博洋" || profile.Position != "副教授" || profile.College != "土木工程学院" || profile.Education != "博士研究生毕业" {
		t.Fatalf("unexpected profile: %+v", profile)
	}
	if len(profile.Research) != 2 || profile.Research[0] != "轮轨滚动接触行为与损伤控制" {
		t.Fatalf("unexpected research directions: %+v", profile.Research)
	}
	if profile.Email != "private@example.com" || profile.OfficeLocation != "不应被输出" || profile.PostalCode != "611756" || profile.PrimaryRole != "副教授" || len(profile.Sections) != 1 || profile.Sections[0].Title != "教育经历" {
		t.Fatalf("contact or detailed sections were not collected: %+v", profile)
	}
	encoded, _ := json.Marshal(profile)
	for _, forbidden := range []string{"邮箱", "通讯/办公地址", "邮编"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("forbidden field leaked: %q in %s", forbidden, encoded)
		}
	}
	if !strings.Contains(string(encoded), "private@example.com") || !strings.Contains(string(encoded), "office_location") {
		t.Fatalf("explicitly labelled contact fields are missing: %s", encoded)
	}
}

func TestParseProfileModernTemplate(t *testing.T) {
	body := []byte(`<!doctype html><html><head><title>西南交通大学教师主页 奥妮--中文主页--首页</title></head><body>
	<div class="name"><span>奥妮</span><span class="zc">副研究员</span></div><img class="teacher-avatar" src="/images/aoni.jpg" alt="奥妮">
<div class="bsd"><div><p>博士生导师</p></div><div><p>硕士生导师</p></div></div>
	<div class="t_jbxx_nr"><p><span>学历：</span><span>博士研究生毕业</span></p><p><span>学位：</span><span>工学博士学位</span></p><p><span>性别：</span><span>女</span></p><p><span>在职信息：</span><span>在岗</span></p><p><span>毕业院校：</span><span>西北工业大学</span></p><p><span>所在单位：</span><span>轨道交通运载系统全国重点实验室</span></p><p>主要任职：特任副教授</p><p>入职时间：2020-12-23</p><p>办公地点：红楼</p><p>邮编：611756</p></div>
<div class="p_r_nr"><h1>个人简介<span>Personal Profile</span></h1><div class="t_grjj_nr"><p>奥妮，工学博士，副研究员。</p></div></div>
<div class="home-bx edubx"><div class="title"><h2>研究方向</h2></div><div class="ct"><p>[1]<a href="/research/1.htm">先进材料及结构完整性</a></p></div></div>
</body></html>`)
	profile, err := ParseProfile(body)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "奥妮" || profile.Position != "副研究员" || profile.College != "轨道交通运载系统全国重点实验室" || profile.PrimaryRole != "特任副教授" || profile.EntryDate != "2020-12-23" || profile.OfficeLocation != "红楼" || profile.PostalCode != "611756" || profile.Research[0] != "先进材料及结构完整性" || profile.AvatarURL != "/images/aoni.jpg" {
		t.Fatalf("unexpected modern profile: %+v", profile)
	}
	resolved, err := ParseProfileAt(body, "https://faculty.swjtu.edu.cn/aoni/zh_CN/index.htm")
	if err != nil || resolved.AvatarURL != "https://faculty.swjtu.edu.cn/images/aoni.jpg" {
		t.Fatalf("avatar was not resolved: %+v err=%v", resolved, err)
	}
}

func TestParseProfileTabbedTemplate(t *testing.T) {
	body := []byte(`<!doctype html><html><head><title>西南交通大学教师主页 Avik Ranjan Adhikary--Chinese homepage--首页</title></head><body>
<div class="name"><span>Avik Ranjan Adhikary</span><span class="zc">讲师（高校）</span></div>
<div id="contentscroll2"><ul><li><strong>学历：</strong>博士研究生毕业</li><li><strong>学位：</strong>理学博士学位</li><li><strong>在职信息：</strong>在岗</li><li><strong>所在单位：</strong>数学学院</li></ul></div>
<ul class="TabbedPanelsTabGroup"><li>个人简介</li><li>研究方向</li><li>社会兼职</li></ul>
<div class="TabbedPanelsContentGroup"><div class="TabbedPanelsContent"><p>数学研究者。</p></div><div class="TabbedPanelsContent"><p><a href="/research/1.htm">Sequence design for communication and radar</a></p></div><div class="TabbedPanelsContent"><p>暂无内容</p></div></div>
</body></html>`)
	profile, err := ParseProfile(body)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "Avik Ranjan Adhikary" || profile.Position != "讲师（高校）" || profile.College != "数学学院" || len(profile.Research) != 1 || profile.Research[0] != "Sequence design for communication and radar" {
		t.Fatalf("unexpected tabbed profile: %+v", profile)
	}
}

func TestParseProfileSlideTemplateAndImageScaleAvatar(t *testing.T) {
	body := []byte(`<!doctype html><html><head><title>西南交通大学教师主页 测试老师--中文主页--首页</title></head><body>
<div class="name"><span>测试老师</span><span class="zc">教授</span></div>
<div class="t_photo"><img id="u_u5_12040pic"><script>var u_u5_pic = new ImageScale("u_u5_",202,242,true,true);u_u5_pic.addimg("/_resources/group1/avatar.png","","测试老师","12040");</script></div>
<div class="t_jbxx_nr"><p>所在单位：计算机学院</p><p>办公地点：犀浦校区</p></div>
<div class="p_r_nr"><h1>个人简介</h1><div class="t_grjj_nr"><p>从事计算机研究。</p></div></div>
<div class="slideTxtBox"><div class="hd"><ul><li>教育经历<span>Education Background</span></li><li>工作经历<span>Work Experience</span></li></ul></div><div class="bd"><ul class="t_edu_nr"><li>西南交通大学，博士</li></ul><ul class="t_edu_nr"><li>西南交通大学，教授</li></ul></div></div>
<div class="slideTxtBox2"><div class="hd"><ul><li>研究方向<span>Research Focus</span></li><li>社会兼职<span>Social Affiliations</span></li></ul></div><div class="bd"><ul><li><a href="/research/1.htm">智能交通</a></li></ul><ul><li>学会委员</li></ul></div></div>
</body></html>`)
	profile, err := ParseProfile(body)
	if err != nil {
		t.Fatal(err)
	}
	if profile.AvatarURL != "/_resources/group1/avatar.png" || profile.OfficeLocation != "犀浦校区" || len(profile.Research) != 1 || profile.Research[0] != "智能交通" {
		t.Fatalf("slide template fields were not parsed: %+v", profile)
	}
	if len(profile.Sections) != 3 {
		t.Fatalf("expected education, work and social sections, got %+v", profile.Sections)
	}
	resolved, err := ParseProfileAt(body, "https://faculty.swjtu.edu.cn/test/zh_CN/index.htm")
	if err != nil || resolved.AvatarURL != "https://faculty.swjtu.edu.cn/_resources/group1/avatar.png" {
		t.Fatalf("ImageScale avatar was not resolved: %+v err=%v", resolved, err)
	}
}

func TestCrawlDeduplicatesAndCrossChecksProfileName(t *testing.T) {
	listURL := "https://faculty.swjtu.edu.cn/pyjslb.jsp?lang=zh_CN&py=a&urltype=tsites.PinYinTeacherList&wbtreeid=1001"
	profileURL := "https://faculty.swjtu.edu.cn/anboyang/zh_CN/index.htm"
	body, err := os.ReadFile(filepath.Join("testdata", "list-a.html"))
	if err != nil {
		t.Fatal(err)
	}
	profileBody, err := os.ReadFile(filepath.Join("testdata", "profile-anboyang.html"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{
		BaseURL: "https://faculty.swjtu.edu.cn", Letters: []string{"a"}, Workers: 2,
		Fetcher: fixtureFetcher{pages: map[string][]byte{listURL: body, profileURL: profileBody}},
		Now:     func() time.Time { return time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Crawl(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.DirectoryEntries != 2 || result.Stats.UniqueTeachers != 2 || result.Stats.DuplicateDirectoryEntries != 0 {
		t.Fatalf("unexpected stats: %+v", result.Stats)
	}
	t.Logf("fixture crawl stats: %+v", result.Stats)
	if result.Stats.ProfilesSucceeded != 1 || result.Stats.ProfilesFailed != 1 {
		t.Fatalf("unexpected profile stats: %+v", result.Stats)
	}
	if result.Teachers[0].Name != "安博洋" || !result.Teachers[0].NameMatch || result.Teachers[0].Status != "ok" {
		t.Fatalf("unexpected successful record: %+v", result.Teachers[0])
	}
	if result.Teachers[0].Email != "private@example.com" || result.Teachers[0].OfficeLocation != "不应被输出" || result.Teachers[0].PostalCode != "611756" || result.Teachers[0].PrimaryRole != "副教授" || result.Teachers[0].AvatarURL != "https://faculty.swjtu.edu.cn/anboyang.jpg" || len(result.Teachers[0].Sections) != 1 {
		t.Fatalf("detailed public profile fields were not handed off: %+v", result.Teachers[0])
	}
	if result.Teachers[1].Status != "error" || result.Teachers[1].Error == "" {
		t.Fatalf("expected failed fixture profile: %+v", result.Teachers[1])
	}
}

func TestCrawlDeduplicatesProfilesAcrossLetters(t *testing.T) {
	listURL := func(letter string) string {
		return "https://faculty.swjtu.edu.cn/pyjslb.jsp?lang=zh_CN&py=" + letter + "&urltype=tsites.PinYinTeacherList&wbtreeid=1001"
	}
	listA, err := os.ReadFile(filepath.Join("testdata", "list-a.html"))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := os.ReadFile(filepath.Join("testdata", "profile-anboyang.html"))
	if err != nil {
		t.Fatal(err)
	}
	pages := map[string][]byte{
		listURL("a"): listA, listURL("b"): listA,
		"https://faculty.swjtu.edu.cn/anboyang/zh_CN/index.htm": profile,
	}
	instance, err := New(Options{BaseURL: "https://faculty.swjtu.edu.cn", Letters: []string{"a", "b"}, Fetcher: fixtureFetcher{pages: pages}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := instance.Crawl(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.DirectoryEntries != 4 || result.Stats.UniqueTeachers != 2 || result.Stats.DuplicateDirectoryEntries != 2 {
		t.Fatalf("cross-letter de-duplication failed: %+v", result.Stats)
	}
}

func TestCrawlFollowsSameLetterPagination(t *testing.T) {
	firstURL := "https://faculty.swjtu.edu.cn/pyjslb.jsp?lang=zh_CN&py=a&urltype=tsites.PinYinTeacherList&wbtreeid=1001"
	secondURL := "https://faculty.swjtu.edu.cn/pyjslb.jsp?lang=zh_CN&page=2&py=a&urltype=tsites.PinYinTeacherList&wbtreeid=1001"
	firstBody, err := os.ReadFile(filepath.Join("testdata", "list-a.html"))
	if err != nil {
		t.Fatal(err)
	}
	firstBody = append(firstBody, []byte(`<a href="?page=2">下一页</a>`)...)
	secondBody := []byte(`<!doctype html><html><body><a href="/aoa/zh_CN/index.htm">欧阳</a></body></html>`)
	instance, err := New(Options{
		BaseURL: "https://faculty.swjtu.edu.cn", Letters: []string{"a"}, ListOnly: true,
		Fetcher: fixtureFetcher{pages: map[string][]byte{firstURL: firstBody, secondURL: secondBody}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := instance.Crawl(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.ListPages != 2 || result.Stats.DirectoryEntries != 3 || result.Stats.UniqueTeachers != 3 {
		t.Fatalf("pagination was not followed: %+v", result.Stats)
	}
}

func TestCrawlFixtureSnapshotAllProfiles(t *testing.T) {
	listURL := "https://faculty.swjtu.edu.cn/pyjslb.jsp?lang=zh_CN&py=a&urltype=tsites.PinYinTeacherList&wbtreeid=1001"
	listBody, err := os.ReadFile(filepath.Join("testdata", "site", "pyjslb.jsp"))
	if err != nil {
		t.Fatal(err)
	}
	anBody, err := os.ReadFile(filepath.Join("testdata", "site", "anboyang", "zh_CN", "index.htm"))
	if err != nil {
		t.Fatal(err)
	}
	adnanBody, err := os.ReadFile(filepath.Join("testdata", "site", "adnanyousaf", "zh_CN", "index.htm"))
	if err != nil {
		t.Fatal(err)
	}
	instance, err := New(Options{
		BaseURL: "https://faculty.swjtu.edu.cn", Letters: []string{"a"}, Workers: 2,
		RawDir: filepath.Join(t.TempDir(), "raw"),
		Fetcher: fixtureFetcher{pages: map[string][]byte{
			listURL: listBody,
			"https://faculty.swjtu.edu.cn/anboyang/zh_CN/index.htm":    anBody,
			"https://faculty.swjtu.edu.cn/adnanyousaf/zh_CN/index.htm": adnanBody,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := instance.Crawl(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.ListPagesFailed != 0 || result.Stats.UniqueTeachers != 2 || result.Stats.ProfilesSucceeded != 2 || result.Stats.ProfilesFailed != 0 || result.Stats.NameMismatches != 0 {
		t.Fatalf("fixture snapshot is not complete: %+v", result.Stats)
	}
	output := filepath.Join(t.TempDir(), "teachers.json")
	if err := WriteJSON(output, result); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "通讯/办公地址") {
		t.Fatal("snapshot contains a raw contact section label")
	}
	t.Logf("complete fixture snapshot: teachers=%d bytes=%d", len(result.Teachers), len(data))
}

func TestWriteJSONIsReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "teachers.json")
	result := CrawlResult{SchemaVersion: SchemaVersion, Teachers: []TeacherRecord{{Name: "测试", ProfileURL: "https://example.test/a", Status: "not_fetched"}}}
	if err := WriteJSON(path, result); err != nil {
		t.Fatal(err)
	}
	var decoded CrawlResult
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Teachers) != 1 || decoded.Teachers[0].Name != "测试" {
		t.Fatalf("unexpected decoded result: %+v", decoded)
	}
}

func TestWriteJSONSanitizesImportedResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "teachers.json")
	result := CrawlResult{Teachers: []TeacherRecord{{
		Name: "测试", ProfileURL: "https://example.test/a", Status: "ok",
		Introduction: "公开简介。电子邮件：yz#swjtu.edu.cn",
		Research:     []string{"方向 secret@example.com"},
	}}}
	if err := WriteJSON(path, result); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "yz#swjtu.edu.cn") || strings.Contains(string(data), "secret@example.com") {
		t.Fatalf("imported snapshot contains contact data: %s", data)
	}
}
