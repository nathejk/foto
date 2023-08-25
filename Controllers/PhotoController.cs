using Microsoft.AspNetCore.Mvc;
using System.Net.Http.Headers;
using SixLabors.ImageSharp.Metadata.Profiles.Exif;
using SixLabors.ImageSharp.Metadata.Profiles.Iptc;

namespace FotoApp.Controllers
{
    [Route("api/[controller]")]
    [ApiController]
    public class PhotoController : ControllerBase
    {
        private readonly IConfiguration _configuration;
        private readonly IHttpContextAccessor _accessor;
        private readonly ILogger<PhotoController> _logger;

        public PhotoController(
            IConfiguration configuration,
            IHttpContextAccessor accessor,
            ILogger<PhotoController> logger
        )
        {
            _configuration = configuration;
            _accessor = accessor;
            _logger = logger;
        }

        [HttpPost]
        public async Task<IActionResult> Upload([FromQuery] string teamNumber)
        {
            try
            {
                Directory.CreateDirectory(_configuration.GetValue<string>("PhotoPath")!);
                Directory.CreateDirectory(_configuration.GetValue<string>("PhotoPath") + "/fb");
                var file = Request.Form.Files[0];
                var pathToSave = _configuration["PhotoPath"];
                if (file.Length <= 0)
                {
                    return BadRequest();
                }

                var extension = Path.GetExtension(
                    ContentDispositionHeaderValue.Parse(file.ContentDisposition).FileName?.Trim('"')
                );

                var prefixCounter = 0;
                string fileName;

                do
                {
                    prefixCounter++;
                    fileName = $"Team-{teamNumber}_{prefixCounter}{extension}";
                    _logger.LogDebug("Incrementing prefixCounter to {Count}", prefixCounter);
                } while (System.IO.File.Exists($"{pathToSave}/{fileName}"));

                var fullPath = Path.Combine(pathToSave!, fileName);
                await using (var stream = new FileStream(fullPath, FileMode.Create))
                {
                    await file.CopyToAsync(stream);
                }

                try
                {
                    using var image = await Image.LoadAsync(fullPath);

                    image.Metadata.IptcProfile ??= new IptcProfile();
                    image.Metadata.IptcProfile.SetValue(IptcTag.BylineTitle, $"Hold {teamNumber}");
                    image.Metadata.IptcProfile.SetValue(IptcTag.Headline, $"Hold {teamNumber}");
                    image.Metadata.IptcProfile.SetValue(IptcTag.Name, $"Hold {teamNumber}");
                    image.Metadata.IptcProfile.SetValue(IptcTag.Caption, $"Hold {teamNumber}");
                    image.Metadata.IptcProfile.SetValue(
                        IptcTag.CopyrightNotice,
                        $"Nathejk {DateTime.Now.Year}"
                    );

                    await image.SaveAsync(fullPath);

                    image.Mutate(
                        x =>
                            x.Resize(
                                new ResizeOptions { Mode = ResizeMode.Max, Size = new Size(2000) }
                            )
                    );

                    await image.SaveAsync(
                        $"{_configuration.GetValue<string>("PhotoPath")}/fb/{fileName}"
                    );
                }
                catch (Exception e)
                {
                    _logger.LogError(e, "Failed resizing image");
                }

                var hostUrl = new Uri(
                    $"{_accessor.HttpContext?.Request.Scheme}://{_accessor.HttpContext?.Request.Host}/photos/{fileName}"
                );

                return new OkObjectResult(new { ImageUrl = hostUrl });
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "Failed saving image!");
                return StatusCode(500, $"Internal server error: {ex}");
            }
        }

        [HttpGet("list")]
        public IActionResult List()
        {
            var hostUrl = new Uri(
                $"{_accessor.HttpContext?.Request.Scheme}://{_accessor.HttpContext?.Request.Host}"
            );
            var localFiles = Directory.GetFiles(_configuration["PhotoPath"] + "/fb");
            var files = localFiles.Select(x => $"{hostUrl}photos/fb/{Path.GetFileName(x)}");

            return Ok(files);
        }
    }
}
