#!/usr/bin/env python3
"""Real Chromium + TLS Server E2E, retaining CSP and all application protections.

Locator assertions auto-wait without eval. Self-signed TLS acceptance applies
only to these disposable browser contexts, never to production CLI pinning.
"""
import argparse
import json
from playwright.sync_api import sync_playwright, expect
from e2e import Harness, ROOT

PASSWORD = 'Browser test password 2026!'
NEW_PASSWORD = 'Changed browser password 2026!'


def run(binary, chromium=None):
    h = Harness(binary, heartbeat='500ms', offline='2s')
    checks = []
    def passed(name):
        checks.append(name)
        print('PASS', name, flush=True)
    try:
        h.start_server()
        h.cli('auth', 'password', 'set', '--password-stdin', '--config', h.admin, stdin=PASSWORD+'\n')
        with sync_playwright() as p:
            launch = {'headless': True}
            if chromium:
                launch['executable_path'] = chromium
            browser = p.chromium.launch(**launch)
            ca = browser.new_context(ignore_https_errors=True, viewport={'width':1440,'height':1000})
            cb = browser.new_context(ignore_https_errors=True, viewport={'width':1300,'height':950})
            a, b = ca.new_page(), cb.new_page()
            errors = []
            try:
                for page in (a, b):
                    page.on('pageerror', lambda e: errors.append(str(e)))
                    page.on('dialog', lambda d: d.accept())
                    page.goto(h.url, wait_until='domcontentloaded')
                    expect(page.locator('#web-password')).to_be_visible()
                    page.locator('#web-password').fill(PASSWORD)
                    page.locator('#login-submit').click()
                    expect(page.locator('#app')).to_be_visible()
                    page.locator('[data-view="note"]').click()
                    expect(page.locator('#note-text')).to_be_enabled()
                passed('Two isolated browsers sign in with a fixed password, no login ticket')
                expect(a.locator('#web-password')).to_have_value('')
                content = '开发说明\nWindows API: localhost:8080\n  原始缩进  '
                a.locator('#note-text').fill(content)
                expect(a.locator('#note-status')).to_contain_text('已保存')
                expect(b.locator('#note-text')).to_have_value(content)
                passed('Textarea autosave reaches a second real browser through SSE')
                a.reload(wait_until='domcontentloaded')
                expect(a.locator('#app')).to_be_visible()
                a.locator('[data-view="note"]').click()
                expect(a.locator('#note-text')).to_have_value(content)
                passed('Reload retains authenticated Cookie and persisted note')
                b.locator('#note-text').dispatch_event('compositionstart')
                b.locator('#note-text').fill('B 尚未提交的本机草稿')
                a.locator('#note-text').fill('A 已提交的新版本')
                expect(b.locator('#note-conflict')).to_be_visible()
                expect(b.locator('#note-text')).to_have_value('B 尚未提交的本机草稿')
                expect(b.locator('#note-remote')).to_have_value('A 已提交的新版本')
                b.locator('#note-text').dispatch_event('compositionend')
                b.locator('#note-keep-local').click()
                expect(a.locator('#note-text')).to_have_value('B 尚未提交的本机草稿')
                passed('Concurrent editing retains draft; explicit resolution uses fresh CAS')
                profile = h.root/'bot.json'
                h.cli('bot', 'bind', '--name', 'Notebook Bot', '--out', profile, '--config', h.admin)
                note = h.cli('note', 'get', '--config', profile)
                hostile = '<img src=x onerror=window.__noteXSS=1>'
                h.cli('note', 'set', '--revision', note['revision'], '--text', hostile, '--config', profile)
                expect(a.locator('#note-text')).to_have_value(hostile)
                expect(a.locator('#view-note img')).to_have_count(0)
                assert a.evaluate('() => window.__noteXSS === undefined')
                passed('Bot CLI shares the same note; text never becomes HTML')
                # Save a useful screenshot after the XSS assertion has completed.
                note = h.cli('note', 'get', '--config', profile)
                screenshot_text = 'Seven-Air-Link · 真实浏览器联调\n\nMac / Windows 开发协同\nAPI: localhost:8080\n\n自动保存 · SSE 同步 · Revision 冲突保护'
                h.cli('note', 'set', '--revision', note['revision'], '--text', screenshot_text, '--config', profile)
                expect(a.locator('#note-text')).to_have_value(screenshot_text)
                a.screenshot(path=str(ROOT/'reports/ui-rc3-notes-live.png'), full_page=True)
                b.set_viewport_size({'width':390,'height':844})
                assert b.evaluate('() => document.documentElement.scrollWidth <= innerWidth')
                b.screenshot(path=str(ROOT/'reports/ui-rc3-notes-mobile-live.png'), full_page=True)
                a.locator('[data-view="settings"]').click()
                expect(a.locator('#session-list .session-card')).to_have_count(2)
                a.locator('#current-web-password').fill(PASSWORD)
                a.locator('#new-web-password').fill(NEW_PASSWORD)
                a.locator('#confirm-web-password').fill(NEW_PASSWORD)
                a.locator('#password-change-form button[type="submit"]').click()
                for page in (a, b):
                    expect(page.locator('#login')).to_be_visible(timeout=10000)
                passed('Password change logs out both Owner sessions and clears UI state')
                expect(a.locator('#note-text')).to_have_value('')
                expect(b.locator('#note-text')).to_have_value('')
                assert h.cli('identity','whoami','--config',profile)['role']=='member'
                a.locator('#web-password').fill(NEW_PASSWORD)
                a.locator('#login-submit').click()
                expect(a.locator('#app')).to_be_visible()
                a.locator('[data-view="settings"]').click()
                a.locator('#revoke-all-sessions').click()
                expect(a.locator('#login')).to_be_visible()
                passed('Own-session revocation does not revoke independent Bot credentials')
                ticket = h.cli('login','create','--config',profile)['ticket']
                b.locator('#mode-ticket').click()
                b.locator('#ticket').fill(ticket)
                b.locator('#login-submit').click()
                expect(b.locator('#app')).to_be_visible()
                b.locator('[data-view="note"]').click()
                expect(b.locator('#note-text')).to_be_enabled()
                b.locator('#note-text').fill('Bot browser verified')
                expect(b.locator('#note-status')).to_contain_text('已保存')
                assert h.cli('note','get','--config',profile)['text']=='Bot browser verified'
                expect(b.locator('#password-settings')).to_be_hidden()
                passed('Ticket login and Member note editing remain compatible')
                assert not errors, errors
            except Exception:
                for name,page in [('a',a),('b',b)]:
                    try:
                        page.screenshot(path=str(ROOT/f'reports/ui-rc3-failure-{name}.png'),full_page=True)
                        (ROOT/f'reports/ui-rc3-failure-{name}.html').write_text(page.content())
                    except Exception:
                        pass
                raise
            finally:
                browser.close()
        return {'status':'PASS','networked':True,'syntheticData':False,'checks':checks,'count':len(checks),'platform':'Chromium with real HTTPS/SSE Server','testTrust':'self-signed certificate accepted only in disposable test browser contexts; CSP and production TLS pinning unchanged'}
    finally:
        h.close()


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--binary',default=ROOT/'bin/seven-air-link')
    parser.add_argument('--chromium')
    args=parser.parse_args()
    (ROOT/'reports').mkdir(exist_ok=True)
    try:
        result=run(args.binary,args.chromium)
    except Exception as error:
        result={'status':'BLOCKED' if 'ERR_BLOCKED_BY_ADMINISTRATOR' in str(error) else 'FAIL','reason':str(error),'policyBypass':False}
        (ROOT/'reports/ui-rc3-live.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
        raise
    (ROOT/'reports/ui-rc3-live.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps(result,ensure_ascii=False))
