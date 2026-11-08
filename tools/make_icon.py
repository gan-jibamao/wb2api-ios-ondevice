#!/usr/bin/env python3
"""生成 Buddy网关 App 图标（1024x1024）。"""
from PIL import Image, ImageDraw, ImageFont

S = 1024
img = Image.new("RGB", (S, S))
d = ImageDraw.Draw(img)

c1 = (20, 26, 54)
c2 = (76, 29, 149)
c3 = (124, 58, 237)
for y in range(S):
    t = y / (S - 1)
    if t < 0.55:
        k = t / 0.55
        col = tuple(int(c1[i] + (c2[i] - c1[i]) * k) for i in range(3))
    else:
        k = (t - 0.55) / 0.45
        col = tuple(int(c2[i] + (c3[i] - c2[i]) * k) for i in range(3))
    d.line([(0, y), (S, y)], fill=col)

bold = "/usr/share/fonts/dejavu/DejaVuSans-Bold.ttf"
f_main = ImageFont.truetype(bold, 300)
f_sub = ImageFont.truetype(bold, 62)


def center_text(text, font, cy, fill, spacing=0):
    if spacing <= 0:
        w = d.textlength(text, font=font)
        d.text(((S - w) / 2, cy), text, font=font, fill=fill)
        return
    widths = [d.textlength(ch, font=font) for ch in text]
    total = sum(widths) + spacing * (len(text) - 1)
    x = (S - total) / 2
    for ch, w in zip(text, widths):
        d.text((x, cy), ch, font=font, fill=fill)
        x += w + spacing


center_text("WB", f_main, S * 0.28, (255, 255, 255))
d.rounded_rectangle([S * 0.32, S * 0.65, S * 0.68, S * 0.65 + 12], radius=6, fill=(167, 139, 250))
center_text("WORKBUDDY GATEWAY", f_sub, S * 0.71, (216, 200, 255), spacing=4)

out = "/var/minis/workspace/wb2api-ios/ios/WB2API/Assets.xcassets/AppIcon.appiconset/icon_1024.png"
img.save(out)
img.resize((256, 256), Image.LANCZOS).save("/tmp/icon_256.png")
print("saved", out)
