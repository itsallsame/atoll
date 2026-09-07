# Astra-style Blender rocket

This prototype follows the publicly described Astra workflow: generate a Blender Python scene, run Blender headlessly, render a visual checkpoint, validate the mesh, and export an STL.

Run:

```bash
/opt/homebrew/bin/blender --background --python build_rocket.py
```

Outputs are written to `output/`:

- `astra-rocket.blend`: editable Blender scene
- `rocket-hero.png`: rendered checkpoint
- `astra-rocket-printable.stl`: fused print mesh
- `validation.txt`: basic manifold and volume checks

The STL is a prototype, not a certified manufacturing file. Inspect scale, wall thickness, overhangs, and slicer output before printing.

## Clay-to-video launch demo

Build the separate six-second control animation without changing the color scene:

```bash
/opt/homebrew/bin/blender --background output/shenzhou5-cz2f.blend --python build_clay_launch_demo.py
/opt/homebrew/bin/blender --background output/shenzhou5-clay-launch.blend --render-anim
/opt/homebrew/bin/ffmpeg -framerate 24 -i output/clay-frames/frame_%04d.png -c:v libx264 -pix_fmt yuv420p -crf 18 output/shenzhou5-clay-launch.mp4
```

The first command creates `output/shenzhou5-clay-launch.blend` and a middle-frame
preview. The second renders a PNG sequence, and the third encodes the MP4. See
`CLAY_VIDEO_WORKFLOW.md` for the generation prompt and reference strategy.
