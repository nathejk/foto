using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Http.Extensions;
using Microsoft.AspNetCore.Mvc;
using System.Net.Http.Headers;

namespace FotoApp.Controllers
{
    [Route("api/[controller]")]
    [ApiController]
    public class PhotoController : ControllerBase
    {
        private readonly IConfiguration _configuration;
        private readonly IHttpContextAccessor _accessor;

        public PhotoController(IConfiguration configuration, IHttpContextAccessor accessor)
        {
            _configuration = configuration;
            _accessor = accessor;
        }

        [HttpPost]
        public IActionResult Upload([FromQuery] string teamNumber)
        {
            try
            {
                
                var file = Request.Form.Files[0];
                var pathToSave = _configuration["PhotoPath"];
                if (file.Length > 0)
                {
                    //var fileName = ContentDispositionHeaderValue.Parse(file.ContentDisposition).FileName.Trim('"');
                    var extension = Path.GetExtension(ContentDispositionHeaderValue.Parse(file.ContentDisposition).FileName.Trim('"'));
                    var fileName = $"{teamNumber}_{DateTime.UtcNow.ToString("yyyyMMdd_HHmmss")}{extension}";
                    var fullPath = Path.Combine(pathToSave, fileName);
                    using (var stream = new FileStream(fullPath, FileMode.Create))
                    {
                        file.CopyTo(stream);
                    }
                    
                    var hostUrl = new Uri($"{_accessor.HttpContext.Request.Scheme}://{_accessor.HttpContext.Request.Host}/photos/{fileName}");
                    
                    return new OkObjectResult(new
                    {
                        ImageUrl = hostUrl
                    });
                }
                else
                {
                    return BadRequest();
                }
            }
            catch (Exception ex)
            {
                return StatusCode(500, $"Internal server error: {ex}");
            }
        }
    }
}
