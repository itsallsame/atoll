import bpy
import bmesh
import math
import os
from mathutils import Vector


ROOT = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(ROOT, "output")
os.makedirs(OUT, exist_ok=True)


def clear_scene():
    bpy.ops.object.select_all(action="SELECT")
    bpy.ops.object.delete(use_global=False)
    for block in (bpy.data.meshes, bpy.data.curves, bpy.data.materials, bpy.data.cameras, bpy.data.lights):
        for item in list(block):
            if item.users == 0:
                block.remove(item)


def material(name, color, metallic=0.0, roughness=0.4, emission=None):
    mat = bpy.data.materials.new(name)
    mat.diffuse_color = (*color, 1.0)
    mat.use_nodes = True
    bsdf = next((node for node in mat.node_tree.nodes if node.type == "BSDF_PRINCIPLED"), None)
    if bsdf is None:
        raise RuntimeError(f"No Principled BSDF node was created for material {name}")
    bsdf.inputs["Base Color"].default_value = (*color, 1.0)
    bsdf.inputs["Metallic"].default_value = metallic
    bsdf.inputs["Roughness"].default_value = roughness
    if emission:
        bsdf.inputs["Emission Color"].default_value = (*emission, 1.0)
        bsdf.inputs["Emission Strength"].default_value = 2.5
    return mat


def apply_bevel(obj, width=0.08, segments=3):
    mod = obj.modifiers.new("Soft manufacturing edges", "BEVEL")
    mod.width = width
    mod.segments = segments
    bpy.context.view_layer.objects.active = obj
    bpy.ops.object.modifier_apply(modifier=mod.name)


def smooth(obj, angle=math.radians(40)):
    if obj.type == "MESH":
        for poly in obj.data.polygons:
            poly.use_smooth = True
        geo = obj.modifiers.new("Weighted normals", "NODES") if False else None


def add_cylinder(name, radius, depth, z, mat, vertices=96, bevel=0.04):
    bpy.ops.mesh.primitive_cylinder_add(vertices=vertices, radius=radius, depth=depth, location=(0, 0, z))
    obj = bpy.context.object
    obj.name = name
    obj.data.materials.append(mat)
    if bevel:
        apply_bevel(obj, bevel)
    smooth(obj)
    return obj


def add_cone(name, radius1, radius2, depth, z, mat, vertices=96, bevel=0.03):
    bpy.ops.mesh.primitive_cone_add(
        vertices=vertices, radius1=radius1, radius2=radius2, depth=depth, location=(0, 0, z)
    )
    obj = bpy.context.object
    obj.name = name
    obj.data.materials.append(mat)
    if bevel:
        apply_bevel(obj, bevel)
    smooth(obj)
    return obj


def add_fin(name, angle, mat):
    # Closed trapezoidal prism, rotated around the rocket's vertical axis.
    r0, r1 = 0.88, 1.78
    z0, z1, z2 = 0.78, 1.16, 2.45
    half_t = 0.115
    section = [(r0, z0), (r1, z0), (r1, z1), (r0, z2)]
    radial = Vector((math.cos(angle), math.sin(angle), 0))
    tangent = Vector((-math.sin(angle), math.cos(angle), 0))
    verts = []
    for side in (-half_t, half_t):
        for radius, z in section:
            p = radial * radius + tangent * side + Vector((0, 0, z))
            verts.append(tuple(p))
    faces = [
        (0, 1, 2, 3), (7, 6, 5, 4),
        (0, 4, 5, 1), (1, 5, 6, 2), (2, 6, 7, 3), (3, 7, 4, 0),
    ]
    mesh = bpy.data.meshes.new(name + "Mesh")
    mesh.from_pydata(verts, [], faces)
    mesh.update()
    obj = bpy.data.objects.new(name, mesh)
    bpy.context.collection.objects.link(obj)
    obj.data.materials.append(mat)
    apply_bevel(obj, 0.055, 3)
    smooth(obj)
    return obj


def add_torus(name, location, major_radius, minor_radius, rotation, mat):
    bpy.ops.mesh.primitive_torus_add(
        major_radius=major_radius,
        minor_radius=minor_radius,
        major_segments=96,
        minor_segments=24,
        location=location,
        rotation=rotation,
    )
    obj = bpy.context.object
    obj.name = name
    obj.data.materials.append(mat)
    smooth(obj)
    return obj


def look_at(obj, target):
    direction = Vector(target) - obj.location
    obj.rotation_euler = direction.to_track_quat("-Z", "Y").to_euler()


def duplicate_for_print(objects):
    print_collection = bpy.data.collections.new("PRINT_EXPORT")
    bpy.context.scene.collection.children.link(print_collection)
    copies = []
    for source in objects:
        dup = source.copy()
        dup.data = source.data.copy()
        dup.animation_data_clear()
        for collection in list(dup.users_collection):
            collection.objects.unlink(dup)
        print_collection.objects.link(dup)
        dup.name = "PRINT_" + source.name
        dup.hide_render = True
        copies.append(dup)

    bpy.ops.object.select_all(action="DESELECT")
    for obj in copies:
        obj.hide_set(False)
        obj.select_set(True)
    bpy.context.view_layer.objects.active = copies[0]
    bpy.ops.object.join()
    joined = bpy.context.object
    joined.name = "Rocket_Print_Mesh"

    # Voxel remeshing fuses intersecting closed parts into one printable shell.
    joined.data.remesh_voxel_size = 0.035
    joined.data.remesh_voxel_adaptivity = 0.0
    bpy.ops.object.voxel_remesh()
    bpy.ops.object.shade_smooth_by_angle()
    return joined


def mesh_report(obj):
    bm = bmesh.new()
    bm.from_mesh(obj.data)
    bm.normal_update()
    non_manifold = sum(1 for edge in bm.edges if not edge.is_manifold)
    volume = abs(bm.calc_volume(signed=True))
    result = {
        "vertices": len(bm.verts),
        "faces": len(bm.faces),
        "non_manifold_edges": non_manifold,
        "volume_blender_units_cubed": round(volume, 5),
    }
    bm.free()
    return result


clear_scene()

red = material("HeatShield Red", (0.55, 0.025, 0.035), metallic=0.45, roughness=0.24)
ivory = material("Ceramic Ivory", (0.82, 0.86, 0.9), metallic=0.18, roughness=0.27)
dark = material("Graphite", (0.018, 0.025, 0.04), metallic=0.75, roughness=0.2)
gold = material("Window Brass", (0.96, 0.49, 0.05), metallic=0.78, roughness=0.18)
glass = material("Warm Window", (0.98, 0.48, 0.04), metallic=0.05, roughness=0.12, emission=(1.0, 0.24, 0.015))
concrete = material("Launchpad", (0.08, 0.1, 0.13), metallic=0.15, roughness=0.62)

core = []
core.append(add_cylinder("Rocket Body", 1.02, 4.25, 3.0, ivory, bevel=0.07))
core.append(add_cone("Nose Cone", 1.04, 0.055, 1.9, 6.02, red, bevel=0.035))
core.append(add_cone("Engine Bell", 0.72, 0.43, 0.75, 0.54, dark, bevel=0.04))
for i, angle in enumerate((0, math.pi / 2, math.pi, math.pi * 1.5), start=1):
    core.append(add_fin(f"Fin {i}", angle, red))

add_cylinder("Lower Accent Ring", 1.075, 0.18, 1.17, gold, bevel=0.025)
add_cylinder("Upper Accent Ring", 1.075, 0.14, 4.77, gold, bevel=0.022)
add_cylinder("Nose Collar", 1.075, 0.16, 5.08, dark, bevel=0.022)

# The yellow circle from the launch-film prompt becomes the rocket's porthole.
add_torus("Porthole Frame", (0, -0.985, 4.05), 0.43, 0.095, (math.pi / 2, 0, 0), gold)
bpy.ops.mesh.primitive_uv_sphere_add(segments=64, ring_count=32, location=(0, -0.995, 4.05))
window = bpy.context.object
window.name = "Yellow Porthole"
window.scale = (0.39, 0.095, 0.39)
window.data.materials.append(glass)
smooth(window)

# Small rivets communicate scale without becoming part of the print mesh.
for idx, a in enumerate(range(0, 360, 45)):
    rad = math.radians(a)
    x = math.cos(rad) * 0.53
    z = 4.05 + math.sin(rad) * 0.53
    y = -1.015
    bpy.ops.mesh.primitive_uv_sphere_add(segments=20, ring_count=12, radius=0.045, location=(x, y, z))
    rivet = bpy.context.object
    rivet.name = f"Porthole Rivet {idx + 1}"
    rivet.data.materials.append(dark)

# Launchpad and a subtle inset ring.
bpy.ops.mesh.primitive_cylinder_add(vertices=96, radius=3.55, depth=0.25, location=(0, 0, 0.0))
pad = bpy.context.object
pad.name = "Launchpad"
pad.data.materials.append(concrete)
apply_bevel(pad, 0.12, 4)
add_torus("Launchpad Light Ring", (0, 0, 0.14), 2.5, 0.035, (0, 0, 0), glass)

# Camera.
bpy.ops.object.camera_add(location=(9.2, -12.2, 7.8))
camera = bpy.context.object
camera.name = "Hero Camera"
camera.data.lens = 57
look_at(camera, (0, 0, 3.25))
bpy.context.scene.camera = camera

# Three-point lighting with a warm key and cool rim.
def area_light(name, location, energy, color, size, target=(0, 0, 3.0)):
    bpy.ops.object.light_add(type="AREA", location=location)
    lamp = bpy.context.object
    lamp.name = name
    lamp.data.energy = energy
    lamp.data.color = color
    lamp.data.shape = "DISK"
    lamp.data.size = size
    look_at(lamp, target)
    return lamp


area_light("Warm Key", (5, -6, 9), 1250, (1.0, 0.55, 0.3), 5.0)
area_light("Cool Fill", (-5, -2, 5), 900, (0.22, 0.42, 1.0), 4.0)
area_light("Rim", (2.5, 5, 8), 1500, (0.45, 0.65, 1.0), 3.0)

world = bpy.context.scene.world
world.use_nodes = True
world.node_tree.nodes["Background"].inputs["Color"].default_value = (0.003, 0.007, 0.022, 1.0)
world.node_tree.nodes["Background"].inputs["Strength"].default_value = 0.22

scene = bpy.context.scene
scene.render.engine = "BLENDER_EEVEE"
scene.render.resolution_x = 900
scene.render.resolution_y = 900
scene.render.resolution_percentage = 100
scene.render.image_settings.file_format = "PNG"
scene.render.film_transparent = False
scene.render.filepath = os.path.join(OUT, "rocket-hero.png")
scene.render.image_settings.color_mode = "RGBA"
scene.view_settings.look = "AgX - Medium High Contrast"

print_mesh = duplicate_for_print(core)
report = mesh_report(print_mesh)

bpy.ops.object.select_all(action="DESELECT")
print_mesh.select_set(True)
bpy.context.view_layer.objects.active = print_mesh
bpy.ops.wm.stl_export(
    filepath=os.path.join(OUT, "astra-rocket-printable.stl"),
    export_selected_objects=True,
    global_scale=25.0,
)

print_mesh.hide_render = True
bpy.ops.wm.save_as_mainfile(filepath=os.path.join(OUT, "astra-rocket.blend"))
bpy.ops.render.render(write_still=True)

with open(os.path.join(OUT, "validation.txt"), "w", encoding="utf-8") as fh:
    fh.write("Astra-style procedural Blender prototype\n")
    for key, value in report.items():
        fh.write(f"{key}: {value}\n")
    fh.write("stl_scale: 25 mm per Blender unit\n")
    fh.write("target_height_mm: approximately 175 mm\n")

print("VALIDATION", report)
print("OUTPUT", OUT)
