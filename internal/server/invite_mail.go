package server

import (
	"bytes"
	"context"
	htmltemplate "html/template"
	texttemplate "text/template"

	"github.com/mcbulazs/restash/internal/mail"
	"github.com/mcbulazs/restash/internal/store"
)

type inviteMailData struct {
	From    string // the inviter's name
	Email   string // who it's for
	Site    string // e.g. restash.bulazs.com
	Link    string
	Expires string
}

var inviteText = texttemplate.Must(texttemplate.New("").Parse(`Hi,

{{.From}} invited you to back up your PC with restash at {{.Site}}.

Make your account here:
{{.Link}}

The link works once, for 7 days (until {{.Expires}}), and only for {{.Email}}.

If you weren't expecting this, you can ignore this email. Replying reaches {{.From}}.
`))

var inviteHTML = htmltemplate.Must(htmltemplate.New("").Parse(`<!doctype html>
<html><body style="margin:0;padding:24px;background:#f6f5f2;font-family:-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#1c1b19;font-size:15px;line-height:1.55">
<div style="max-width:480px;margin:0 auto;background:#fff;border-radius:12px;padding:28px">
<p style="margin:0 0 16px"><strong>{{.From}}</strong> invited you to back up your PC with restash at {{.Site}}.</p>
<p style="margin:0 0 22px"><a href="{{.Link}}" style="display:inline-block;background:#2747d9;color:#fff;text-decoration:none;padding:11px 18px;border-radius:8px;font-weight:600">Make your account</a></p>
<p style="margin:0 0 16px;color:#5f5c55;font-size:13px">The link works once, for 7 days (until {{.Expires}}), and only for {{.Email}}. If the button doesn't work, open this link:<br><a href="{{.Link}}" style="color:#2747d9;word-break:break-all">{{.Link}}</a></p>
<p style="margin:0;color:#5f5c55;font-size:13px">If you weren't expecting this, you can ignore this email. Replying reaches {{.From}}.</p>
</div>
</body></html>
`))

// sendInvite emails an invite link. Replies go to the person who invited.
func (s *Server) sendInvite(ctx context.Context, inv *store.Invite, from *store.User) error {
	d := inviteMailData{From: from.Name, Email: inv.Email, Site: s.cfg.PublicURL.Host, Link: s.inviteLink(inv.Token), Expires: inv.ExpiresAt.Format("2 January 2006")}
	if d.From == "" {
		d.From = from.Email
	}
	var text, html bytes.Buffer
	if err := inviteText.Execute(&text, d); err != nil {
		return err
	}
	if err := inviteHTML.Execute(&html, d); err != nil {
		return err
	}
	return s.mail.Send(ctx, mail.Message{
		To:      inv.Email,
		ReplyTo: from.Email,
		Subject: d.From + " invited you to restash",
		Text:    text.String(),
		HTML:    html.String(),
	})
}
