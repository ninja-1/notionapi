package tohtml

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"path"
	"strings"
	"time"

	"github.com/kjk/notionapi"
)

// BlockRenderFunc is a function for rendering a particular
type BlockRenderFunc func(block *notionapi.Block, entering bool) bool

// HTMLRenderer converts a Page to HTML
type HTMLRenderer struct {
	// Buf is where HTML is being written to
	Buf  *bytes.Buffer
	Page *notionapi.Page

	// if true, adds id=${NotionID} attribute to HTML nodes
	AddIDAttribute bool

	// mostly for debugging. If true will panic when encounters
	// structure it cannot handle (e.g. when Notion adds another
	// type of block)
	PanicOnFailures bool

	// allows over-riding rendering of specific blocks
	// return false for default rendering
	RenderBlockOverride BlockRenderFunc

	RenderInlineLinkOverride func(*notionapi.InlineBlock) (string, bool)

	// data provided by they caller, useful when providing
	// RenderBlockOverride
	Data interface{}

	// mostly for debugging, if set we'll log to it when encountering
	// structure we can't handle
	Log func(format string, args ...interface{})

	// Level is current depth of the tree. Useuful for pretty-printing indentation
	Level int

	// we need this to properly render ordered and numbered lists
	CurrBlocks   []*notionapi.Block
	CurrBlockIdx int

	// keeps a nesting stack of numbered / bulleted list
	// we need this because they are not nested in data model
	ListStack []string

	bufs []*bytes.Buffer
}

var (
	selfClosingTags = map[string]bool{
		"img": true,
	}
)

func isSelfClosing(tag string) bool {
	return selfClosingTags[tag]
}

// NewHTMLRenderer returns customizable HTML renderer
func NewHTMLRenderer(page *notionapi.Page) *HTMLRenderer {
	return &HTMLRenderer{
		Page: page,
	}
}

// TODO: not sure if I want to keep this or always use maybePanic
// (which also logs)
func (r *HTMLRenderer) log(format string, args ...interface{}) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

func (r *HTMLRenderer) maybePanic(format string, args ...interface{}) {
	if r.Log != nil {
		r.Log(format, args...)
	}
	if r.PanicOnFailures {
		panic(fmt.Sprintf(format, args...))
	}
}

// PushNewBuffer creates a new buffer and sets Buf to it
func (r *HTMLRenderer) PushNewBuffer() {
	r.bufs = append(r.bufs, r.Buf)
	r.Buf = &bytes.Buffer{}
}

// PopBuffer pops a buffer
func (r *HTMLRenderer) PopBuffer() *bytes.Buffer {
	res := r.Buf
	n := len(r.bufs)
	r.Buf = r.bufs[n-1]
	r.bufs = r.bufs[:n-1]
	return res
}

// Newline writes a newline to the buffer. It'll suppress multiple newlines.
func (r *HTMLRenderer) Newline() {
	d := r.Buf.Bytes()
	n := len(d)
	if n > 0 && d[n-1] != '\n' {
		r.Buf.WriteByte('\n')
	}
}

// WriteString writes a string to the buffer
func (r *HTMLRenderer) WriteString(s string) {
	r.Buf.WriteString(s)
}

// WriteIndentPlus writes 2 * (Level + add) spaces
func (r *HTMLRenderer) WriteIndentPlus(add int) {
	for n := 0; n < r.Level+add; n++ {
		r.WriteString("  ")
	}
}

// WriteIndent writes 2 * Level spaces
func (r *HTMLRenderer) WriteIndent() {
	if r.Level < 0 {
		panic("r.Level is < 0")
	}
	for n := 0; n < r.Level; n++ {
		r.WriteString("  ")
	}
}

func (r *HTMLRenderer) maybeGetID(block *notionapi.Block) string {
	if r.AddIDAttribute {
		return notionapi.ToNoDashID(block.ID)
	}
	return ""
}

// WriteElement is a helper class that writes HTML with
// attributes and optional content
func (r *HTMLRenderer) WriteElement(block *notionapi.Block, tag string, attrs []string, content string, entering bool) {
	if !entering {
		if !isSelfClosing(tag) {
			r.WriteIndent()
			r.WriteString("</" + tag + ">")
			r.Newline()
		}
		return
	}

	s := "<" + tag
	nAttrs := len(attrs) / 2
	for i := 0; i < nAttrs; i++ {
		a := attrs[i*2]
		// TODO: quote value if necessary
		v := attrs[i*2+1]
		s += fmt.Sprintf(` %s="%s"`, a, v)
	}
	id := r.maybeGetID(block)
	if id != "" {
		s += ` id="` + id + `"`
	}
	s += ">"
	r.WriteIndent()
	r.WriteString(s)
	r.Newline()
	if len(content) > 0 {
		r.WriteIndent()
		r.WriteString(content)
		r.Newline()
	}
	r.RenderInlines(block.InlineContent)
	r.Newline()
}

// PrevBlock is a block preceding current block
func (r *HTMLRenderer) PrevBlock() *notionapi.Block {
	if r.CurrBlockIdx == 0 {
		return nil
	}
	return r.CurrBlocks[r.CurrBlockIdx-1]
}

// NextBlock is a block preceding current block
func (r *HTMLRenderer) NextBlock() *notionapi.Block {
	nextIdx := r.CurrBlockIdx + 1
	lastIdx := len(r.CurrBlocks) - 1
	if nextIdx > lastIdx {
		return nil
	}
	return r.CurrBlocks[nextIdx]
}

// IsPrevBlockOfType returns true if previous block is of a given type
func (r *HTMLRenderer) IsPrevBlockOfType(t string) bool {
	b := r.PrevBlock()
	if b == nil {
		return false
	}
	return b.Type == t
}

// IsNextBlockOfType returns true if next block is of a given type
func (r *HTMLRenderer) IsNextBlockOfType(t string) bool {
	b := r.NextBlock()
	if b == nil {
		return false
	}
	return b.Type == t
}

// ParseNotionDateTime parses date and time as sent in JSON by notion
// server and returns time.Time
// date is sent in "2019-04-09" format
// time is optional and sent in "00:35" format
func (r *HTMLRenderer) ParseNotionDateTime(date string, t string) time.Time {
	s := date
	fmt := "2006-01-02"
	if t != "" {
		fmt += " 15:04"
		s += " " + t
	}
	dt, err := time.Parse(fmt, s)
	if err != nil {
		r.maybePanic("time.Parse('%s', '%s') failed with %s", fmt, s, err)
	}
	return dt
}

// ConvertNotionTimeFormatToGoFormat converts a date format sent from Notion
// server, e.g. "MMM DD, YYYY" to Go time format like "02 01, 2006"
// YYYY is numeric year => 2006 in Go
// MM is numeric month => 01 in Go
// DD is numeric day => 02 in Go
// MMM is named month => Jan in Go
func (r *HTMLRenderer) ConvertNotionTimeFormatToGoFormat(d *notionapi.Date, withTime bool) string {
	format := d.DateFormat
	// we don't support relative time, so use this fixed format
	if format == "relative" || format == "" {
		format = "MMM DD, YYYY"
	}
	s := format
	s = strings.Replace(s, "MMM", "Jan", -1)
	s = strings.Replace(s, "MM", "01", -1)
	s = strings.Replace(s, "DD", "02", -1)
	s = strings.Replace(s, "YYYY", "2006", -1)
	if withTime {
		// this is 24 hr format
		if d.TimeFormat == "H:mm" {
			s += " 15:04"
		} else {
			// use 12 hr format
			s += " 3:04 PM"
		}
	}
	return s
}

// FormatDateTime formats date/time from Notion canonical format to
// user-requested format
func (r *HTMLRenderer) FormatDateTime(d *notionapi.Date, date string, t string) string {
	withTime := t != ""
	dt := r.ParseNotionDateTime(date, t)
	goFormat := r.ConvertNotionTimeFormatToGoFormat(d, withTime)
	return dt.Format(goFormat)
}

// DefaultFormatDate is default formatting of date
// "date_format": "relative",
// "start_date": "2019-03-26",
// "type": "date"
// TODO: add time zone, maybe
func (r *HTMLRenderer) DefaultFormatDate(d *notionapi.Date) string {
	s := r.FormatDateTime(d, d.StartDate, d.StartTime)
	if strings.Contains(d.Type, "range") {
		s2 := r.FormatDateTime(d, d.EndDate, d.EndTime)
		s += " → " + s2
	}
	return fmt.Sprintf(`<span class="notion-date">@%s</span>`, s)
}

// FormatDate formats the data
func (r *HTMLRenderer) FormatDate(d *notionapi.Date) string {
	// TODO: allow over-riding date formatting
	return r.DefaultFormatDate(d)
}

// DefaultRenderInlineLink returns default HTML for inline links
func DefaultRenderInlineLink(b *notionapi.InlineBlock) string {
	link := b.Link
	text := html.EscapeString(b.Text)
	return fmt.Sprintf(`<a class="notion-link" href="%s">%s</a>`, link, text)
}

// RenderInline renders inline block
func (r *HTMLRenderer) RenderInline(b *notionapi.InlineBlock) {
	var start, close string
	if b.AttrFlags&notionapi.AttrBold != 0 {
		start += "<b>"
		close += "</b>"
	}
	if b.AttrFlags&notionapi.AttrItalic != 0 {
		start += "<i>"
		close += "</i>"
	}
	if b.AttrFlags&notionapi.AttrStrikeThrought != 0 {
		start += "<strike>"
		close += "</strike>"
	}
	if b.AttrFlags&notionapi.AttrCode != 0 {
		start += "<code>"
		close += "</code>"
	}
	skipText := false
	// TODO: colors
	if b.Link != "" {
		s := DefaultRenderInlineLink(b)
		if r.RenderInlineLinkOverride != nil {
			if s2, ok := r.RenderInlineLinkOverride(b); ok {
				s = s2
			}
		}
		start += s
		skipText = true
	}
	if b.UserID != "" {
		start += fmt.Sprintf(`<span class="notion-user">@TODO: user with id%s</span>`, b.UserID)
		skipText = true
	}
	if b.Date != nil {
		start += r.FormatDate(b.Date)
		skipText = true
	}
	if !skipText {
		start += b.Text
	}
	r.WriteString(start + close)
}

// RenderInlines renders inline blocks
func (r *HTMLRenderer) RenderInlines(blocks []*notionapi.InlineBlock) {
	r.Level++
	r.WriteIndent()
	bufLen := r.Buf.Len()
	for _, block := range blocks {
		r.RenderInline(block)
	}

	// if text was empty, write &nbsp; so that empty blocks show up
	if bufLen == r.Buf.Len() {
		r.WriteString("&nbsp;")
	}
	r.Level--
}

// GetInlineContent is like RenderInlines but instead of writing to
// output buffer, we return it as string
func (r *HTMLRenderer) GetInlineContent(blocks []*notionapi.InlineBlock) string {
	if len(blocks) == 0 {
		return "&nbsp;"
	}
	r.PushNewBuffer()
	for _, block := range blocks {
		r.RenderInline(block)
	}
	// if text was empty, write &nbsp; so that empty blocks show up
	if r.Buf.Len() == 0 {
		r.WriteString("&nbsp;")
	}
	return r.PopBuffer().String()
}

// RenderCode renders BlockCode
func (r *HTMLRenderer) RenderCode(block *notionapi.Block, entering bool) bool {
	if !entering {
		r.WriteString("</code></pre>")
		r.Newline()
		return true
	}
	cls := "notion-code"
	lang := strings.ToLower(strings.TrimSpace(block.CodeLanguage))
	if lang != "" {
		cls += " notion-lang-" + lang
	}
	code := template.HTMLEscapeString(block.Code)
	s := fmt.Sprintf(`<pre class="%s"><code>%s`, cls, code)
	r.WriteString(s)
	return true
}

// RenderPage renders BlockPage
func (r *HTMLRenderer) RenderPage(block *notionapi.Block, entering bool) bool {
	tp := block.GetPageType()
	if tp == notionapi.BlockPageTopLevel {
		title := template.HTMLEscapeString(block.Title)
		content := fmt.Sprintf(`<div class="notion-page-content">%s</div>`, title)
		attrs := []string{"class", "notion-page"}
		r.WriteElement(block, "div", attrs, content, entering)
		return true
	}

	if !entering {
		return true
	}

	cls := "notion-page-link"
	if tp == notionapi.BlockPageSubPage {
		cls = "notion-sub-page"
	}
	id := notionapi.ToNoDashID(block.ID)
	uri := "https://notion.so/" + id
	title := template.HTMLEscapeString(block.Title)
	s := fmt.Sprintf(`<div class="%s"><a href="%s">%s</a></div>`, cls, uri, title)
	r.WriteIndent()
	r.WriteString(s)
	r.Newline()
	return true
}

// RenderText renders BlockText
func (r *HTMLRenderer) RenderText(block *notionapi.Block, entering bool) bool {
	attrs := []string{"class", "notion-text"}
	r.WriteElement(block, "div", attrs, "", entering)
	return true
}

// RenderNumberedList renders BlockNumberedList
func (r *HTMLRenderer) RenderNumberedList(block *notionapi.Block, entering bool) bool {
	if entering {
		isPrevSame := r.IsPrevBlockOfType(notionapi.BlockNumberedList)
		if !isPrevSame {
			r.WriteIndent()
			r.WriteString(`<ol class="notion-numbered-list">`)
		}
		attrs := []string{"class", "notion-numbered-list"}
		r.WriteElement(block, "li", attrs, "", entering)
	} else {
		r.WriteIndent()
		r.WriteString(`</li>`)
		isNextSame := r.IsNextBlockOfType(notionapi.BlockNumberedList)
		if !isNextSame {
			r.WriteIndent()
			r.WriteString(`</ol>`)
		}
		r.Newline()
	}
	return true
}

// RenderBulletedList renders BlockBulletedList
func (r *HTMLRenderer) RenderBulletedList(block *notionapi.Block, entering bool) bool {

	if entering {
		isPrevSame := r.IsPrevBlockOfType(notionapi.BlockBulletedList)
		if !isPrevSame {
			r.WriteIndent()
			r.WriteString(`<ul class="notion-bulleted-list">`)
			r.Newline()
			r.Level++
		}
		attrs := []string{"class", "notion-bulleted-list"}
		r.WriteElement(block, "li", attrs, "", entering)
	} else {
		r.WriteIndent()
		r.WriteString(`</li>`)
		isNextSame := r.IsNextBlockOfType(notionapi.BlockBulletedList)
		if !isNextSame {
			r.Level--
			r.Newline()
			r.WriteIndent()
			r.WriteString(`</ul>`)
		}
		r.Newline()
	}
	return true
}

// RenderHeaderLevel renders BlockHeader, SubHeader and SubSubHeader
func (r *HTMLRenderer) RenderHeaderLevel(block *notionapi.Block, level int, entering bool) bool {
	el := fmt.Sprintf("h%d", level)
	cls := fmt.Sprintf("notion-header-%d", level)
	attrs := []string{"class", cls}
	r.WriteElement(block, el, attrs, "", entering)
	return true
}

// RenderHeader renders BlockHeader
func (r *HTMLRenderer) RenderHeader(block *notionapi.Block, entering bool) bool {
	return r.RenderHeaderLevel(block, 1, entering)
}

// RenderSubHeader renders BlockSubHeader
func (r *HTMLRenderer) RenderSubHeader(block *notionapi.Block, entering bool) bool {
	return r.RenderHeaderLevel(block, 2, entering)
}

// RenderSubSubHeader renders BlocSubSubkHeader
func (r *HTMLRenderer) RenderSubSubHeader(block *notionapi.Block, entering bool) bool {
	return r.RenderHeaderLevel(block, 3, entering)
}

// RenderTodo renders BlockTodo
func (r *HTMLRenderer) RenderTodo(block *notionapi.Block, entering bool) bool {
	cls := "notion-todo"
	if block.IsChecked {
		cls = "notion-todo-checked"
	}
	attrs := []string{"class", cls}
	r.WriteElement(block, "div", attrs, "", entering)
	return true
}

// RenderToggle renders BlockToggle
func (r *HTMLRenderer) RenderToggle(block *notionapi.Block, entering bool) bool {
	if entering {
		attrs := []string{"class", "notion-toggle"}
		r.WriteElement(block, "div", attrs, "", entering)

		s := `<div class="notion-toggle-wrapper">`
		r.WriteString(s)
		r.Newline()
	} else {
		s := `</div>`
		r.WriteString(s)
		r.Newline()
		attrs := []string{"class", "notion-toggle"}
		r.WriteElement(block, "div", attrs, "", entering)
	}

	return true
}

// RenderQuote renders BlockQuote
func (r *HTMLRenderer) RenderQuote(block *notionapi.Block, entering bool) bool {
	cls := "notion-quote"
	attrs := []string{"class", cls}
	r.WriteElement(block, "quote", attrs, "", entering)
	return true
}

// RenderDivider renders BlockDivider
func (r *HTMLRenderer) RenderDivider(block *notionapi.Block, entering bool) bool {
	if !entering {
		return true
	}
	r.WriteString(`<hr class="notion-divider">`)
	r.Newline()
	return true
}

// RenderBookmark renders BlockBookmark
func (r *HTMLRenderer) RenderBookmark(block *notionapi.Block, entering bool) bool {
	content := fmt.Sprintf(`<a href="%s">%s</a>`, block.Link, block.Link)
	cls := "notion-bookmark"
	// TODO: don't render inlines (which seems to be title of the bookmarked page)
	// TODO: support caption
	// TODO: maybe support comments
	attrs := []string{"class", cls}
	r.WriteElement(block, "div", attrs, content, entering)
	return true
}

// RenderVideo renders BlockTweet
func (r *HTMLRenderer) RenderVideo(block *notionapi.Block, entering bool) bool {
	f := block.FormatVideo
	ws := fmt.Sprintf("%d", f.BlockWidth)
	uri := f.DisplaySource
	if uri == "" {
		// TODO: not sure if this is needed
		uri = block.Source
	}
	// TODO: get more info from format
	attrs := []string{
		"class", "notion-video",
		"width", ws,
		"src", uri,
		"frameborder", "0",
		"allow", "encrypted-media",
		"allowfullscreen", "true",
	}
	// TODO: can it be that f.BlockWidth is 0 and we need to
	// calculate it from f.BlockHeight
	h := f.BlockHeight
	if h == 0 {
		h = int64(float64(f.BlockWidth) * f.BlockAspectRatio)
	}
	if h > 0 {
		hs := fmt.Sprintf("%d", h)
		attrs = append(attrs, "height", hs)
	}

	r.WriteElement(block, "iframe", attrs, "", entering)
	return true
}

// RenderTweet renders BlockTweet
func (r *HTMLRenderer) RenderTweet(block *notionapi.Block, entering bool) bool {
	uri := block.Source
	content := fmt.Sprintf(`Embedded tweet <a href="%s">%s</a>`, uri, uri)
	cls := "notion-embed"
	// TODO: don't render inlines (which seems to be title of the bookmarked page)
	// TODO: support caption
	// TODO: maybe support comments
	attrs := []string{"class", cls}
	r.WriteElement(block, "div", attrs, content, entering)
	return true
}

// RenderGist renders BlockGist
func (r *HTMLRenderer) RenderGist(block *notionapi.Block, entering bool) bool {
	uri := block.Source + ".js"
	cls := "notion-embed-gist"
	attrs := []string{"src", uri, "class", cls}
	// TODO: support caption
	// TODO: maybe support comments
	r.WriteElement(block, "script", attrs, "", entering)
	return true
}

// RenderEmbed renders BlockEmbed
func (r *HTMLRenderer) RenderEmbed(block *notionapi.Block, entering bool) bool {
	// TODO: best effort at making the URL readable
	uri := block.FormatEmbed.DisplaySource
	title := block.Title
	if title == "" {
		title = path.Base(uri)
	}
	title = html.EscapeString(title)
	content := fmt.Sprintf(`Oembed: <a href="%s">%s</a>`, uri, title)
	cls := "notion-embed"
	attrs := []string{"class", cls}
	r.WriteElement(block, "div", attrs, content, entering)
	return true
}

// RenderFile renders BlockFile
func (r *HTMLRenderer) RenderFile(block *notionapi.Block, entering bool) bool {
	// TODO: best effort at making the URL readable
	uri := block.Source
	title := block.Title
	if title == "" {
		title = path.Base(uri)
	}
	title = html.EscapeString(title)
	content := fmt.Sprintf(`Embedded file: <a href="%s">%s</a>`, uri, title)
	cls := "notion-embed"
	attrs := []string{"class", cls}
	r.WriteElement(block, "div", attrs, content, entering)
	return true
}

// RenderPDF renders BlockPDF
func (r *HTMLRenderer) RenderPDF(block *notionapi.Block, entering bool) bool {
	// TODO: best effort at making the URL readable
	uri := block.Source
	title := block.Title
	if title == "" {
		title = path.Base(uri)
	}
	title = html.EscapeString(title)
	content := fmt.Sprintf(`Embedded PDF: <a href="%s">%s</a>`, uri, title)
	cls := "notion-embed"
	attrs := []string{"class", cls}
	r.WriteElement(block, "div", attrs, content, entering)
	return true
}

// RenderImage renders BlockImage
func (r *HTMLRenderer) RenderImage(block *notionapi.Block, entering bool) bool {
	link := block.ImageURL
	attrs := []string{"class", "notion-image", "src", link}
	r.WriteElement(block, "img", attrs, "", entering)
	return true
}

// RenderColumnList renders BlockColumnList
// it's children are BlockColumn
func (r *HTMLRenderer) RenderColumnList(block *notionapi.Block, entering bool) bool {
	nColumns := len(block.Content)
	if nColumns == 0 {
		r.maybePanic("has no columns")
		return true
	}
	attrs := []string{"class", "notion-column-list"}
	r.WriteElement(block, "div", attrs, "", entering)
	return true
}

// RenderColumn renders BlockColumn
// it's parent is BlockColumnList
func (r *HTMLRenderer) RenderColumn(block *notionapi.Block, entering bool) bool {
	// TODO: get column ration from col.FormatColumn.ColumnRation, which is float 0...1
	attrs := []string{"class", "notion-column"}
	r.WriteElement(block, "div", attrs, "", entering)
	return true
}

// RenderCollectionView renders BlockCollectionView
// TODO: it renders all views, should render just one
// TODO: maybe add alternating background color for rows
func (r *HTMLRenderer) RenderCollectionView(block *notionapi.Block, entering bool) bool {
	viewInfo := block.CollectionViews[0]
	view := viewInfo.CollectionView
	columns := view.Format.TableProperties

	r.Newline()
	r.WriteIndent()
	r.WriteString("\n" + `<table class="notion-collection-view">` + "\n")

	// generate header row
	r.Level++
	r.WriteIndent()
	r.WriteString("<thead>\n")

	r.Level++
	r.WriteIndent()
	r.WriteString("<tr>\n")

	for _, col := range columns {
		colName := col.Property
		colInfo := viewInfo.Collection.CollectionSchema[colName]
		name := colInfo.Name
		r.Level++
		r.WriteIndent()
		r.WriteString(`<th>` + html.EscapeString(name) + "</th>\n")
		r.Level--
	}
	r.WriteIndent()
	r.WriteString("</tr>\n")

	r.Level--
	r.WriteIndent()
	r.WriteString("</thead>\n\n")

	r.WriteIndent()
	r.WriteString("<tbody>\n")

	for _, row := range viewInfo.CollectionRows {
		r.Level++
		r.WriteIndent()
		r.WriteString("<tr>\n")

		props := row.Properties
		for _, col := range columns {
			colName := col.Property
			v := props[colName]
			//fmt.Printf("inline: '%s'\n", fmt.Sprintf("%v", v))
			inlineContent, err := notionapi.ParseInlineBlocks(v)
			if err != nil {
				r.maybePanic("ParseInlineBlocks of '%v' failed with %s\n", v, err)
			}
			//pretty.Print(inlineContent)
			colVal := r.GetInlineContent(inlineContent)
			//fmt.Printf("colVal: '%s'\n", colVal)
			r.Level++
			r.WriteIndent()
			//colInfo := viewInfo.Collection.CollectionSchema[colName]
			// TODO: format colVal according to colInfo
			r.WriteString(`<td>` + colVal + `</td>`)
			r.Newline()
			r.Level--
		}
		r.WriteIndent()
		r.WriteString("</tr>\n")
		r.Level--
	}

	r.WriteIndent()
	r.WriteString("</tbody>\n")

	r.Level--
	r.WriteIndent()
	r.WriteString("</table>\n")
	return true
}

// DefaultRenderFunc returns a defult rendering function for a type of
// a given block
func (r *HTMLRenderer) DefaultRenderFunc(blockType string) BlockRenderFunc {
	switch blockType {
	case notionapi.BlockPage:
		return r.RenderPage
	case notionapi.BlockText:
		return r.RenderText
	case notionapi.BlockNumberedList:
		return r.RenderNumberedList
	case notionapi.BlockBulletedList:
		return r.RenderBulletedList
	case notionapi.BlockHeader:
		return r.RenderHeader
	case notionapi.BlockSubHeader:
		return r.RenderSubHeader
	case notionapi.BlockSubSubHeader:
		return r.RenderSubSubHeader
	case notionapi.BlockTodo:
		return r.RenderTodo
	case notionapi.BlockToggle:
		return r.RenderToggle
	case notionapi.BlockQuote:
		return r.RenderQuote
	case notionapi.BlockDivider:
		return r.RenderDivider
	case notionapi.BlockCode:
		return r.RenderCode
	case notionapi.BlockBookmark:
		return r.RenderBookmark
	case notionapi.BlockImage:
		return r.RenderImage
	case notionapi.BlockColumnList:
		return r.RenderColumnList
	case notionapi.BlockColumn:
		return r.RenderColumn
	case notionapi.BlockCollectionView:
		return r.RenderCollectionView
	case notionapi.BlockEmbed:
		return r.RenderEmbed
	case notionapi.BlockGist:
		return r.RenderGist
	case notionapi.BlockTweet:
		return r.RenderTweet
	case notionapi.BlockVideo:
		return r.RenderVideo
	case notionapi.BlockFile:
		return r.RenderFile
	case notionapi.BlockPDF:
		return r.RenderPDF
	default:
		r.maybePanic("DefaultRenderFunc: unsupported block type '%s' in %s\n", blockType, r.Page.NotionURL())
	}
	return nil
}

func needsWrapper(block *notionapi.Block) bool {
	if len(block.Content) == 0 {
		return false
	}
	switch block.Type {
	// TODO: maybe more block types need this
	case notionapi.BlockText:
		return true
	}
	return false
}

// RenderBlock renders a block to html
func (r *HTMLRenderer) RenderBlock(block *notionapi.Block) {
	if block == nil {
		// a missing block
		return
	}
	def := r.DefaultRenderFunc(block.Type)
	handled := false
	if r.RenderBlockOverride != nil {
		handled = r.RenderBlockOverride(block, true)
	}
	if !handled && def != nil {
		def(block, true)
	}

	// .notion-wrap provides indentation for children
	if needsWrapper(block) {
		r.Newline()
		r.WriteIndent()
		r.WriteString(`<div class="notion-wrap">`)
		r.Newline()
	}

	r.Level++
	currIdx := r.CurrBlockIdx
	currBlocks := r.CurrBlocks
	r.CurrBlocks = block.Content
	for i, child := range block.Content {
		child.Parent = block
		r.CurrBlockIdx = i
		r.RenderBlock(child)
	}
	r.CurrBlockIdx = currIdx
	r.CurrBlocks = currBlocks
	r.Level--

	if needsWrapper(block) {
		r.Newline()
		r.WriteIndent()
		r.WriteString(`</div>`)
		r.Newline()
	}

	handled = false
	if r.RenderBlockOverride != nil {
		handled = r.RenderBlockOverride(block, false)
	}
	if !handled && def != nil {
		def(block, false)
	}
}

// ToHTML renders a page to html
func (r *HTMLRenderer) ToHTML() []byte {
	r.Level = 0
	r.PushNewBuffer()

	r.RenderBlock(r.Page.Root)
	buf := r.PopBuffer()
	if r.Level != 0 {
		panic(fmt.Sprintf("r.Level is %d, should be 0", r.Level))
	}
	return buf.Bytes()
}

// ToHTML converts a page to HTML
func ToHTML(page *notionapi.Page) []byte {
	r := NewHTMLRenderer(page)
	return r.ToHTML()
}
