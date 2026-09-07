# Shenzhou 5 clay-to-video demo

The Blender scene is a six-second, 24 fps control animation. It deliberately
contains neutral materials and simple geometry so a video model can inherit the
camera, blocking and motion without being forced to preserve the preview look.

## Control render

- 1280x720, 24 fps, frames 1-144
- 1.5 second hold, ignition, then accelerating lift-off
- rapid push-in followed by an ultra-close, top-down, wide-angle orbit
- deterministic engine vibration after ignition for onboard-camera energy
- geometric plume and expanding pad-smoke guides
- original color scene remains unchanged

## Suggested generation prompt

Use the clay-render video as the strict reference for camera movement, shot
timing, rocket trajectory, scale, blocking and smoke expansion. Transform it
into a photorealistic cinematic launch of Shenzhou 5 on a Long March 2F rocket
at Jiuquan Satellite Launch Center at dawn. Preserve the exact rocket silhouette
and four boosters. Dense white and tan exhaust rolls across the pad; a bright
orange-white engine plume appears at ignition; heat distortion, vibration and
fine airborne dust are visible. Cool blue ambient sky, warm sunrise rim light,
physically plausible shadows, documentary aerospace photography, restrained
film grain. Do not add or remove rocket stages, boosters, towers or vehicles.
No text, captions, logos or watermarks.

## Reference strategy

Use the MP4 for motion and composition. Add the color hero image as a style and
vehicle-identity reference. Generate several short variants, then restore exact
Chinese markings from the Blender render during compositing if needed.
