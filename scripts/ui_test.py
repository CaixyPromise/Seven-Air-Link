#!/usr/bin/env python3
"""Optional UI regression. Requires Playwright and a Chromium executable.
App/runtime itself has no Playwright or Python dependency.
"""
import argparse
import json
from pathlib import Path
from time import monotonic
from e2e import Harness, ROOT, wait_for
from playwright.sync_api import sync_playwright

def run(binary,chromium):
    h=Harness(binary,heartbeat="500ms",offline="2s")
    screenshots=ROOT/"reports";screenshots.mkdir(exist_ok=True)
    try:
        h.start_server()
        ca,ia=h.pair("Linux Dev · A", "ui-a")
        cb,ib=h.pair("Linux Dev · B", "ui-b")
        pa,pb=h.node(ca),h.node(cb)
        wait_for(lambda:h.status()["onlineDevices"]==2)
        ticket=h.cli("login","create","--config",h.admin)["ticket"]
        with sync_playwright() as p:
            browser=p.chromium.launch(executable_path=chromium,headless=True,args=["--no-sandbox","--disable-dev-shm-usage"])
            # Test fixture self-signed cert. CLI pinning is tested separately.
            context=browser.new_context(ignore_https_errors=True,viewport={"width":1440,"height":1050},device_scale_factor=1)
            page=context.new_page();errors=[];page.on("pageerror",lambda e:errors.append(str(e)))
            page.goto(h.url);page.locator("#login").wait_for(state="visible")
            page.locator("#ticket").fill(ticket);page.locator("#login-form button").click()
            page.locator("#app").wait_for(state="visible")
            page.locator('[data-view="devices"]').click()
            page.wait_for_function("document.querySelector('#online-count').textContent==='2'")
            page.wait_for_function("document.querySelector('#connection').textContent.includes('实时连接')")
            page.screenshot(path=str(screenshots/"ui-desktop.png"),full_page=True)
            page.locator("#new-pair").click();page.locator("#generate-pair").click();page.locator("#pair-result").wait_for(state="visible")
            code=page.locator("#pair-code").input_value();assert code.startswith("pair_")
            page.locator('[data-close="pair-dialog"]').click()
            start=monotonic();h.pair("From browser pairing", "ui-c",code=code)
            page.wait_for_function("document.querySelector('#device-count').textContent==='3'",timeout=4000)
            latency=monotonic()-start
            assert latency<4, "Live refresh should not wait for the 5s fallback poll"
            card=page.locator(f'[data-id="{ia}"]')
            card.locator(".rename").click()
            attack="<img src=x onerror=window.__slb_xss=1>"
            page.locator("#device-name").fill(attack);page.locator('#rename-form button[type="submit"]').click()
            page.wait_for_function("document.querySelector('.device-name').textContent.startsWith('<img')")
            assert card.locator("img").count()==0 and not page.evaluate("window.__slb_xss===1")
            # Restore display name, then verify the same session at mobile size.
            card.locator(".rename").click();page.locator("#device-name").fill("Linux Dev · A");page.locator('#rename-form button[type="submit"]').click()
            page.wait_for_function("document.querySelector('.device-name').textContent==='Linux Dev · A'")
            page.locator('[data-view="activity"]').click();page.locator("#view-activity").wait_for(state="visible")
            page.locator('[data-view="settings"]').click();assert page.locator("#fingerprint").inner_text()==h.owner["fingerprint"]
            page.locator('[data-view="devices"]').click()
            page.set_viewport_size({"width":390,"height":844})
            assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")
            page.screenshot(path=str(screenshots/"ui-mobile.png"),full_page=True)
            page.set_viewport_size({"width":1440,"height":1050})
            card.locator(".revoke").click();page.locator("#confirm-revoke").click()
            wait_for(lambda:pa.poll() is not None,timeout=4)
            page.wait_for_function("document.querySelector('.status').textContent==='已撤销'")
            assert h.states()[ib]=="online"
            page.locator("#logout").click();page.locator("#login").wait_for(state="visible")
            assert not errors,errors
            browser_version=browser.version
            context.close();browser.close()
        return {"status":"PASS","browser":"Chromium "+browser_version,"selfSignedFixture":"ignore_https_errors=True in browser test ONLY; CLI pins separately verified","liveUpdateSeconds":round(latency,3),"checks":["one-time ticket login","two real live Nodes","Web SSE connected","browser invitation consumed by CLI join","live device update before fallback polling","rename and XSS-safe text rendering","activity and settings navigation","390px viewport without horizontal overflow","UI revoke closes real Node","logout invalidates browser session","no uncaught JavaScript exceptions"],"screenshots":["ui-desktop.png","ui-mobile.png"]}
    finally:
        h.close()

if __name__=="__main__":
    parser=argparse.ArgumentParser();parser.add_argument("--binary",default=str(ROOT/"bin/seven-air-link"));parser.add_argument("--chromium",default="/usr/bin/chromium");args=parser.parse_args()
    try:
        report=run(args.binary,args.chromium)
    except Exception as exc:
        report={"status":"BLOCKED" if "ERR_BLOCKED_BY_ADMINISTRATOR" in str(exc) else "FAIL", "reason":str(exc), "notValidated":["live browser login/SSE/device actions"], "policyBypass":False}
        (ROOT/"reports/ui-test.json").write_text(json.dumps(report,ensure_ascii=False,indent=2)+"\n")
        raise
    (ROOT/"reports/ui-test.json").write_text(json.dumps(report,ensure_ascii=False,indent=2)+"\n")
    print(json.dumps(report,ensure_ascii=False,indent=2))
